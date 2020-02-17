package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof" // Register pprof handlers with DefaultServeMux.
	"net/url"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"

	auth "github.com/abbot/go-http-auth"
	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk"
	"github.com/buchgr/bazel-remote/cache/gcs"
	"github.com/buchgr/bazel-remote/cache/s3"

	cachehttp "github.com/buchgr/bazel-remote/cache/http"

	"github.com/buchgr/bazel-remote/config"
	"github.com/buchgr/bazel-remote/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v2"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

const (
	logFlags = log.Ldate | log.Ltime | log.LUTC
)

// gitCommit is the version stamp for the server. The value of this var
// is set through linker options.
var gitCommit string

func main() {

	log.SetFlags(logFlags)

	maybeGitCommitMsg := ""
	if len(gitCommit) > 0 && gitCommit != "{STABLE_GIT_COMMIT}" {
		maybeGitCommitMsg = fmt.Sprintf(" from git commit %s", gitCommit)
	}
	log.Printf("bazel-remote built with %s%s.",
		runtime.Version(), maybeGitCommitMsg)

	app := cli.NewApp()
	app.Description = "A remote build cache for Bazel."
	app.Usage = "A remote build cache for Bazel"
	app.HideVersion = true

	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:  "config_file",
			Value: "",
			Usage: "Path to a YAML configuration file. If this flag is specified then all other flags " +
				"are ignored.",
			EnvVars: []string{"BAZEL_REMOTE_CONFIG_FILE"},
		},
		&cli.StringFlag{
			Name:    "dir",
			Value:   "",
			Usage:   "Directory path where to store the cache contents. This flag is required.",
			EnvVars: []string{"BAZEL_REMOTE_DIR"},
		},
		&cli.Int64Flag{
			Name:    "max_size",
			Value:   -1,
			Usage:   "The maximum size of the remote cache in GiB. This flag is required.",
			EnvVars: []string{"BAZEL_REMOTE_MAX_SIZE"},
		},
		&cli.StringFlag{
			Name:    "host",
			Value:   "",
			Usage:   "Address to listen on. Listens on all network interfaces by default.",
			EnvVars: []string{"BAZEL_REMOTE_HOST"},
		},
		&cli.IntFlag{
			Name:    "port",
			Value:   8080,
			Usage:   "The port the HTTP server listens on.",
			EnvVars: []string{"BAZEL_REMOTE_PORT"},
		},
		&cli.IntFlag{
			Name:    "grpc_port",
			Value:   9092,
			Usage:   "The port the EXPERIMENTAL gRPC server listens on. Set to 0 to disable.",
			EnvVars: []string{"BAZEL_REMOTE_GRPC_PORT"},
		},
		&cli.StringFlag{
			Name:    "profile_host",
			Value:   "127.0.0.1",
			Usage:   "A host address to listen on for profiling, if enabled by a valid --profile_port setting.",
			EnvVars: []string{"BAZEL_REMOTE_PROFILE_HOST"},
		},
		&cli.IntFlag{
			Name:    "profile_port",
			Value:   0,
			Usage:   "If a positive integer, serve /debug/pprof/* URLs from http://profile_host:profile_port.",
			EnvVars: []string{"BAZEL_REMOTE_PROFILE_PORT"},
		},
		&cli.StringFlag{
			Name:    "htpasswd_file",
			Value:   "",
			Usage:   "Path to a .htpasswd file. This flag is optional. Please read https://httpd.apache.org/docs/2.4/programs/htpasswd.html.",
			EnvVars: []string{"BAZEL_REMOTE_HTPASSWD_FILE"},
		},
		&cli.BoolFlag{
			Name:    "tls_enabled",
			Usage:   "This flag has been deprecated. Specify tls_cert_file and tls_key_file instead.",
			EnvVars: []string{"BAZEL_REMOTE_TLS_ENABLED"},
		},
		&cli.StringFlag{
			Name:    "tls_cert_file",
			Value:   "",
			Usage:   "Path to a pem encoded certificate file.",
			EnvVars: []string{"BAZEL_REMOTE_TLS_CERT_FILE"},
		},
		&cli.StringFlag{
			Name:    "tls_key_file",
			Value:   "",
			Usage:   "Path to a pem encoded key file.",
			EnvVars: []string{"BAZEL_REMOTE_TLS_KEY_FILE"},
		},
		&cli.DurationFlag{
			Name:    "idle_timeout",
			Value:   0,
			Usage:   "The maximum period of having received no request after which the server will shut itself down. Disabled by default.",
			EnvVars: []string{"BAZEL_REMOTE_IDLE_TIMEOUT"},
		},
		&cli.StringFlag{
			Name:    "s3.endpoint",
			Value:   "",
			Usage:   "The S3/minio endpoint to use when using S3 cache backend.",
			EnvVars: []string{"BAZEL_REMOTE_S3_ENDPOINT"},
		},
		&cli.StringFlag{
			Name:    "s3.bucket",
			Value:   "",
			Usage:   "The S3/minio bucket to use when using S3 cache backend.",
			EnvVars: []string{"BAZEL_REMOTE_S3_BUCKET"},
		},
		&cli.StringFlag{
			Name:    "s3.prefix",
			Value:   "",
			Usage:   "The S3/minio object prefix to use when using S3 cache backend.",
			EnvVars: []string{"BAZEL_REMOTE_S3_PREFIX"},
		},
		&cli.StringFlag{
			Name:    "s3.access_key_id",
			Value:   "",
			Usage:   "The S3/minio access key to use when using S3 cache backend.",
			EnvVars: []string{"BAZEL_REMOTE_S3_ACCESS_KEY_ID"},
		},
		&cli.StringFlag{
			Name:    "s3.secret_access_key",
			Value:   "",
			Usage:   "The S3/minio secret access key to use when using S3 cache backend.",
			EnvVars: []string{"BAZEL_REMOTE_S3_SECRET_ACCESS_KEY"},
		},
		&cli.BoolFlag{
			Name:    "s3.disable_ssl",
			Usage:   "Whether to disable TLS/SSL when using the S3 cache backend.  Default is false (enable TLS/SSL).",
			EnvVars: []string{"BAZEL_REMOTE_S3_DISABLE_SSL"},
		},
		&cli.StringFlag{
			Name:    "s3.iam_role_endpoint",
			Value:   "",
			Usage:   "Endpoint for using IAM security credentials, eg http://169.254.169.254 for EC2, http://169.254.170.2 for ECS",
			EnvVars: []string{"BAZEL_REMOTE_S3_IAM_ROLE_ENDPOINT"},
		},
		&cli.StringFlag{
			Name:    "s3.region",
			Value:   "",
			Usage:   "The AWS region. Required when using s3.iam_role_endpoint",
			EnvVars: []string{"BAZEL_REMOTE_S3_REGION"},
		},
		&cli.BoolFlag{
			Name:    "disable_http_ac_validation",
			Usage:   "Whether to disable ActionResult validation for HTTP requests.  Default is false (enable validation).",
			EnvVars: []string{"BAZEL_REMOTE_DISABLE_HTTP_AC_VALIDATION"},
		},
	}

	app.Action = func(ctx *cli.Context) error {
		configFile := ctx.String("config_file")
		var c *config.Config
		var err error
		if configFile != "" {
			c, err = config.NewFromYamlFile(configFile)
		} else {
			var s3 *config.S3CloudStorageConfig
			if ctx.String("s3.bucket") != "" {
				s3 = &config.S3CloudStorageConfig{
					Endpoint:        ctx.String("s3.endpoint"),
					Bucket:          ctx.String("s3.bucket"),
					Prefix:          ctx.String("s3.prefix"),
					AccessKeyID:     ctx.String("s3.access_key_id"),
					SecretAccessKey: ctx.String("s3.secret_access_key"),
					DisableSSL:      ctx.Bool("s3.disable_ssl"),
					IAMRoleEndpoint: ctx.String("s3.iam_role_endpoint"),
					Region:          ctx.String("s3.region"),
				}
			}
			c, err = config.New(
				ctx.String("dir"),
				ctx.Int("max_size"),
				ctx.String("host"),
				ctx.Int("port"),
				ctx.Int("grpc_port"),
				ctx.String("profile_host"),
				ctx.Int("profile_port"),
				ctx.String("htpasswd_file"),
				ctx.String("tls_cert_file"),
				ctx.String("tls_key_file"),
				ctx.Duration("idle_timeout"),
				s3,
				ctx.Bool("disable_http_ac_validation"),
			)
		}

		if err != nil {
			fmt.Fprintf(ctx.App.Writer, "%v\n\n", err)
			cli.ShowAppHelp(ctx)
			return nil
		}

		adjustRlimit()

		accessLogger := log.New(os.Stdout, "", logFlags)
		errorLogger := log.New(os.Stderr, "", logFlags)

		var proxyCache cache.CacheProxy
		if c.GoogleCloudStorage != nil {
			proxyCache, err = gcs.New(c.GoogleCloudStorage.Bucket,
				c.GoogleCloudStorage.UseDefaultCredentials, c.GoogleCloudStorage.JSONCredentialsFile,
				accessLogger, errorLogger)
			if err != nil {
				log.Fatal(err)
			}
		} else if c.HTTPBackend != nil {
			httpClient := &http.Client{}
			var baseURL *url.URL
			baseURL, err = url.Parse(c.HTTPBackend.BaseURL)
			if err != nil {
				log.Fatal(err)
			}
			proxyCache = cachehttp.New(baseURL,
				httpClient, accessLogger, errorLogger)
		} else if c.S3CloudStorage != nil {
			proxyCache = s3.New(c.S3CloudStorage, accessLogger, errorLogger)
		}

		diskCache := disk.New(c.Dir, int64(c.MaxSize)*1024*1024*1024, proxyCache)

		mux := http.NewServeMux()
		httpServer := &http.Server{
			Addr:    c.Host + ":" + strconv.Itoa(c.Port),
			Handler: mux,
		}
		validateAC := !c.DisableHTTPACValidation
		h := server.NewHTTPCache(diskCache, accessLogger, errorLogger, validateAC, gitCommit)
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/status", h.StatusPageHandler)

		cacheHandler := h.CacheHandler
		if c.HtpasswdFile != "" {
			cacheHandler = wrapAuthHandler(cacheHandler, c.HtpasswdFile, c.Host)
		}
		if c.IdleTimeout > 0 {
			cacheHandler = wrapIdleHandler(cacheHandler, c.IdleTimeout, accessLogger, httpServer)
		}
		mux.HandleFunc("/", cacheHandler)

		if c.GRPCPort > 0 {

			if c.GRPCPort == c.Port {
				log.Fatalf("Error: gRPC and HTTP ports (%d) conflict", c.Port)
			}

			go func() {
				addr := c.Host + ":" + strconv.Itoa(c.GRPCPort)

				opts := []grpc.ServerOption{}

				if len(c.TLSCertFile) > 0 && len(c.TLSKeyFile) > 0 {
					creds, err2 := credentials.NewServerTLSFromFile(
						c.TLSCertFile, c.TLSKeyFile)
					if err2 != nil {
						log.Fatal(err2)
					}
					opts = append(opts, grpc.Creds(creds))
				}

				log.Printf("Starting gRPC server on address %s", addr)

				err3 := server.ListenAndServeGRPC(addr, opts,
					diskCache, accessLogger, errorLogger)
				if err3 != nil {
					log.Fatal(err3)
				}
			}()
		}

		if c.ProfilePort > 0 {
			go func() {
				// Allow access to /debug/pprof/ URLs.
				profileAddr := c.ProfileHost + ":" +
					strconv.Itoa(c.ProfilePort)
				log.Printf("Starting HTTP server for profiling on address %s",
					profileAddr)
				log.Fatal(http.ListenAndServe(profileAddr, nil))
			}()
		}

		if len(c.TLSCertFile) > 0 && len(c.TLSKeyFile) > 0 {
			log.Printf("Starting HTTPS server on address %s", httpServer.Addr)
			return httpServer.ListenAndServeTLS(c.TLSCertFile, c.TLSKeyFile)
		}

		log.Printf("Starting HTTP server on address %s", httpServer.Addr)
		return httpServer.ListenAndServe()
	}

	serverErr := app.Run(os.Args)
	if serverErr != nil {
		log.Fatal("bazel-remote terminated: ", serverErr)
	}
}

func wrapIdleHandler(handler http.HandlerFunc, idleTimeout time.Duration, accessLogger cache.Logger, httpServer *http.Server) http.HandlerFunc {
	lastRequest := time.Now()
	ticker := time.NewTicker(time.Second)
	var mu sync.Mutex

	go func() {
		for now := range ticker.C {
			mu.Lock()
			elapsed := now.Sub(lastRequest)
			mu.Unlock()
			if elapsed > idleTimeout {
				ticker.Stop()
				accessLogger.Printf("Shutting down server after having been idle for %v", idleTimeout)
				httpServer.Shutdown(context.Background())
				return
			}
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		mu.Lock()
		lastRequest = now
		mu.Unlock()
		handler(w, r)
	})
}

func wrapAuthHandler(handler http.HandlerFunc, htpasswdFile string, host string) http.HandlerFunc {
	secrets := auth.HtpasswdFileProvider(htpasswdFile)
	authenticator := auth.NewBasicAuthenticator(host, secrets)
	return auth.JustCheck(authenticator, handler)
}
