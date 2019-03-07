package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	auth "github.com/abbot/go-http-auth"
	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk"
	"github.com/buchgr/bazel-remote/cache/gcs"

	cachehttp "github.com/buchgr/bazel-remote/cache/http"

	"github.com/buchgr/bazel-remote/config"
	"github.com/buchgr/bazel-remote/server"
	"github.com/nightlyone/lockfile"
	"github.com/urfave/cli"
)

const bazelRemotePidFile = "bazel-remote.pid"

var signalHandlers []func(os.Signal)
var signalHandlersMutex sync.Mutex

//http server.go doesn't export tcpKeepAliveListener so we have to do the same here
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (net.Conn, error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return nil, err
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	return tc, nil
}

func init() {
	// set up a signal handler to clean up if we are interrupted
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-c:
			signalHandlersMutex.Lock()
			defer signalHandlersMutex.Unlock()
			for _, fn := range signalHandlers {
				fn(sig)
			}
		}
	}()
}

func main() {
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
	}

	app.Action = func(ctx *cli.Context) error {
		configFile := ctx.String("config_file")
		var c *config.Config
		var err error
		if configFile != "" {
			c, err = config.NewFromYamlFile(configFile)
		} else {
			c, err = config.New(ctx.String("dir"),
				ctx.Int("max_size"),
				ctx.String("host"),
				ctx.Int("port"),
				ctx.String("htpasswd_file"),
				ctx.String("tls_cert_file"),
				ctx.String("tls_key_file"),
				ctx.Duration("idle_timeout"))
		}

		if err != nil {
			fmt.Fprintf(ctx.App.Writer, "%v\n\n", err)
			cli.ShowAppHelp(ctx)
			return nil
		}

		accessLogger := log.New(os.Stdout, "", log.Ldate|log.Ltime|log.LUTC)
		errorLogger := log.New(os.Stderr, "", log.Ldate|log.Ltime|log.LUTC)

		if err := os.MkdirAll(c.Dir, 0755); err != nil {
			return err
		}
		lockAbsPath, err := filepath.Abs(filepath.Join(c.Dir, bazelRemotePidFile))
		if err != nil {
			return err
		}
		pidFileLock, err := lockfile.New(lockAbsPath)
		if err != nil {
			return err
		}
		err = pidFileLock.TryLock()
		if err != nil {
			if err == lockfile.ErrBusy {
				pid, _ := ioutil.ReadFile(lockAbsPath)
				return fmt.Errorf(
					"Already locked by pid %v",
					strings.Trim(string(pid), "\n"),
				)
			}
			return fmt.Errorf("Could not lock %v: %v", c.Dir, err)
		}
		defer pidFileLock.Unlock()

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
		} else {
			proxyCache = diskCache
		}

		mux := http.NewServeMux()
		httpServer := &http.Server{
			Handler: mux,
		}

		// graceful shutdown on signal
		func() {
			signalHandlersMutex.Lock()
			defer signalHandlersMutex.Unlock()
			signalHandlers = append(signalHandlers, func(sig os.Signal) {
				errorLogger.Printf("Shutting down server due to signal: %v", sig)
				if err := httpServer.Shutdown(context.Background()); err != nil {
					errorLogger.Printf("Failed to shutdown http server: %v", err)
				}
			})
		}()

		h := server.NewHTTPCache(proxyCache, accessLogger, errorLogger)
		mux.HandleFunc("/status", h.StatusPageHandler)

		cacheHandler := h.CacheHandler
		if c.HtpasswdFile != "" {
			cacheHandler = wrapAuthHandler(cacheHandler, c.HtpasswdFile, c.Host)
		}

		if c.IdleTimeout > 0 {
			cacheHandler = wrapIdleHandler(cacheHandler, c.IdleTimeout, accessLogger, httpServer)
		}

		mux.HandleFunc("/", cacheHandler)
		ln, err := net.Listen("tcp", c.Host+":"+strconv.Itoa(c.Port))
		if err != nil {
			return err
		}
		defer ln.Close()

		// create a unix domain socket and respond with the port when asked
		sock, err := net.Listen("unix", lockAbsPath+".sock")
		if err != nil {
			return err
		}
		defer os.Remove(lockAbsPath + ".sock")
		go handlePortRequest(sock, ln.Addr(), errorLogger)

		if len(c.TLSCertFile) > 0 && len(c.TLSKeyFile) > 0 {
			return httpServer.ServeTLS(tcpKeepAliveListener{ln.(*net.TCPListener)}, c.TLSCertFile, c.TLSKeyFile)
		}
		return httpServer.Serve(tcpKeepAliveListener{ln.(*net.TCPListener)})
	}
	serverErr := app.Run(os.Args)
	if serverErr != nil {
		log.Fatal("bazel-remote terminated: ", serverErr)
	}
}

func handlePortRequest(sock net.Listener, addr net.Addr, errorLogger *log.Logger) {
	for {
		fd, err := sock.Accept()
		if err != nil {
			errorLogger.Printf("sock: %v", err)
			continue
		}
		if _, err := fd.Write([]byte(addr.String() + "\n")); err != nil {
			errorLogger.Printf("sock write: %v", err)
		}
		if err := fd.Close(); err != nil {
			errorLogger.Printf("sock close: %v", err)
		}
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
