package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
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
	"github.com/urfave/cli"

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

	if len(gitCommit) > 0 && gitCommit != "{STABLE_GIT_COMMIT}" {
		log.Printf("bazel-remote built from git commit %s.", gitCommit)
	}

	app := cli.NewApp()
	app.Description = "A remote build cache for Bazel."
	app.Usage = "A remote build cache for Bazel"
	app.HideVersion = true

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "config_file",
			Value: "",
			Usage: "Path to a YAML configuration file. If this flag is specified then all other flags " +
				"are ignored.",
			EnvVar: "BAZEL_REMOTE_CONFIG_FILE",
		},
		cli.StringFlag{
			Name:   "dir",
			Value:  "",
			Usage:  "Directory path where to store the cache contents. This flag is required.",
			EnvVar: "BAZEL_REMOTE_DIR",
		},
		cli.Int64Flag{
			Name:   "max_size",
			Value:  -1,
			Usage:  "The maximum size of the remote cache in GiB. This flag is required.",
			EnvVar: "BAZEL_REMOTE_MAX_SIZE",
		},
		cli.StringFlag{
			Name:   "host",
			Value:  "",
			Usage:  "Address to listen on. Listens on all network interfaces by default.",
			EnvVar: "BAZEL_REMOTE_HOST",
		},
		cli.IntFlag{
			Name:   "port",
			Value:  8080,
			Usage:  "The port the HTTP server listens on.",
			EnvVar: "BAZEL_REMOTE_PORT",
		},
		cli.IntFlag{
			Name:   "grpc_port",
			Value:  9092,
			Usage:  "The port the EXPERIMENTAL gRPC server listens on. Set to 0 to disable.",
			EnvVar: "BAZEL_REMOTE_GRPC_PORT",
		},
		cli.StringFlag{
			Name:   "htpasswd_file",
			Value:  "",
			Usage:  "Path to a .htpasswd file. This flag is optional. Please read https://httpd.apache.org/docs/2.4/programs/htpasswd.html.",
			EnvVar: "BAZEL_REMOTE_HTPASSWD_FILE",
		},
		cli.BoolFlag{
			Name:   "tls_enabled",
			Usage:  "This flag has been deprecated. Specify tls_cert_file and tls_key_file instead.",
			EnvVar: "BAZEL_REMOTE_TLS_ENABLED",
		},
		cli.StringFlag{
			Name:   "tls_cert_file",
			Value:  "",
			Usage:  "Path to a pem encoded certificate file.",
			EnvVar: "BAZEL_REMOTE_TLS_CERT_FILE",
		},
		cli.StringFlag{
			Name:   "tls_key_file",
			Value:  "",
			Usage:  "Path to a pem encoded key file.",
			EnvVar: "BAZEL_REMOTE_TLS_KEY_FILE",
		},
		cli.DurationFlag{
			Name:   "idle_timeout",
			Value:  0,
			Usage:  "The maximum period of having received no request after which the server will shut itself down. Disabled by default.",
			EnvVar: "BAZEL_REMOTE_IDLE_TIMEOUT",
		},
		cli.StringFlag{
			Name:   "s3.endpoint",
			Value:  "",
			Usage:  "The S3/minio endpoint to use when using S3 cache backend.",
			EnvVar: "BAZEL_REMOTE_S3_ENDPOINT",
		},
		cli.StringFlag{
			Name:   "s3.bucket",
			Value:  "",
			Usage:  "The S3/minio bucket to use when using S3 cache backend.",
			EnvVar: "BAZEL_REMOTE_S3_BUCKET",
		},
		cli.StringFlag{
			Name:   "s3.prefix",
			Value:  "",
			Usage:  "The S3/minio object prefix to use when using S3 cache backend.",
			EnvVar: "BAZEL_REMOTE_S3_PREFIX",
		},
		cli.StringFlag{
			Name:   "s3.access_key_id",
			Value:  "",
			Usage:  "The S3/minio access key to use when using S3 cache backend.",
			EnvVar: "BAZEL_REMOTE_S3_ACCESS_KEY_ID",
		},
		cli.StringFlag{
			Name:   "s3.secret_access_key",
			Value:  "",
			Usage:  "The S3/minio secret access key to use when using S3 cache backend.",
			EnvVar: "BAZEL_REMOTE_S3_SECRET_ACCESS_KEY",
		},
		cli.BoolFlag{
			Name:   "s3.disable_ssl",
			Usage:  "Whether to disable TLS/SSL when using the S3 cache backend.  Default is false (enable TLS/SSL).",
			EnvVar: "BAZEL_REMOTE_S3_DISABLE_SSL",
		},
		cli.BoolFlag{
			Name:   "disable_http_ac_validation",
			Usage:  "Whether to disable ActionResult validation for HTTP requests.  Default is false (enable validation).",
			EnvVar: "BAZEL_REMOTE_DISABLE_HTTP_AC_VALIDATION",
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
				}
			}
			c, err = config.New(
				ctx.String("dir"),
				ctx.Int("max_size"),
				ctx.String("host"),
				ctx.Int("port"),
				ctx.Int("grpc_port"),
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

		accessLogger := log.New(os.Stdout, "", logFlags)
		errorLogger := log.New(os.Stderr, "", logFlags)

		diskCache := disk.New(c.Dir, int64(c.MaxSize)*1024*1024*1024)

		var proxyCache cache.Cache
		if c.GoogleCloudStorage != nil {
			proxyCache, err = gcs.New(c.GoogleCloudStorage.Bucket,
				c.GoogleCloudStorage.UseDefaultCredentials, c.GoogleCloudStorage.JSONCredentialsFile,
				diskCache, accessLogger, errorLogger)
			if err != nil {
				log.Fatal(err)
			}
		} else if c.HTTPBackend != nil {
			httpClient := &http.Client{}
			baseURL, err := url.Parse(c.HTTPBackend.BaseURL)
			if err != nil {
				log.Fatal(err)
			}
			proxyCache = cachehttp.New(baseURL, diskCache,
				httpClient, accessLogger, errorLogger)
		} else if c.S3CloudStorage != nil {
			proxyCache = s3.New(c.S3CloudStorage, diskCache, accessLogger, errorLogger)
		} else {
			proxyCache = diskCache
		}

		mux := http.NewServeMux()
		httpServer := &http.Server{
			Addr:    c.Host + ":" + strconv.Itoa(c.Port),
			Handler: mux,
		}
		validateAC := !c.DisableHTTPACValidation
		h := server.NewHTTPCache(proxyCache, accessLogger, errorLogger, validateAC, gitCommit)
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
					creds, err := credentials.NewServerTLSFromFile(
						c.TLSCertFile, c.TLSKeyFile)
					if err != nil {
						log.Fatal(err)
					}
					opts = append(opts, grpc.Creds(creds))
				}

				log.Printf("Starting gRPC server on address %s", addr)

				err = server.ListenAndServeGRPC(addr, opts,
					proxyCache, accessLogger, errorLogger)
				if err != nil {
					log.Fatal(err)
				}
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
	var m sync.Mutex
	go func() {
		for {
			select {
			case now := <-ticker.C:
				m.Lock()
				elapsed := now.Sub(lastRequest)
				m.Unlock()
				if elapsed > idleTimeout {
					ticker.Stop()
					accessLogger.Printf("Shutting down server after having been idle for %v", idleTimeout)
					httpServer.Shutdown(context.Background())
				}
			}
		}
	}()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		m.Lock()
		lastRequest = now
		m.Unlock()
		handler(w, r)
	})
}

func wrapAuthHandler(handler http.HandlerFunc, htpasswdFile string, host string) http.HandlerFunc {
	secrets := auth.HtpasswdFileProvider(htpasswdFile)
	authenticator := auth.NewBasicAuthenticator(host, secrets)
	return auth.JustCheck(authenticator, handler)
}
