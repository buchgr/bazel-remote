package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
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
	"github.com/urfave/cli"
)

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
		cli.BoolFlag{
			Name:   "kill_old_pid",
			Hidden: false,
			Usage:  "This will kill the existing running bazel-remote process before starting a new bazel-remote process. This is when user want to upgrade with a new version",
			EnvVar: "BAZEL_REMOTE_KILL_OLD",
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
				ctx.Duration("idle_timeout"),
				ctx.Bool("kill_old_pid"))
		}

		if err != nil {
			fmt.Fprintf(ctx.App.Writer, "%v\n\n", err)
			cli.ShowAppHelp(ctx)
			return nil
		}

		accessLogger := log.New(os.Stdout, "", log.Ldate|log.Ltime|log.LUTC)
		errorLogger := log.New(os.Stderr, "", log.Ldate|log.Ltime|log.LUTC)

		writePidFile(c, accessLogger)

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
			Addr:    c.Host + ":" + strconv.Itoa(c.Port),
			Handler: mux,
		}
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

		if len(c.TLSCertFile) > 0 && len(c.TLSKeyFile) > 0 {
			return httpServer.ListenAndServeTLS(c.TLSCertFile, c.TLSKeyFile)
		}
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

func writePidFile(c *config.Config, accessLogger cache.Logger) error {
	// create a "pid" directory under cache directory and clean up inactive pid file
	pidPath := filepath.Join(c.Dir, "pid")
	err := os.MkdirAll(pidPath, os.FileMode(0744))
	if err != nil {
		log.Fatal(err)
		return err
	}
	err = filepath.Walk(pidPath, func(name string, fileInfo os.FileInfo, err error) error {
		if !fileInfo.IsDir() {
			pid, _ := strconv.Atoi(fileInfo.Name())
			bazelRemoteProcess, err := os.FindProcess(pid)
			if err != nil {
				accessLogger.Printf("Error to find pid: %d", bazelRemoteProcess)
			} else {
				err = bazelRemoteProcess.Signal(syscall.Signal(0))
				if err != nil && strings.Contains(err.Error(), "process already finished") {
					accessLogger.Printf("Removing pid file: %s", fileInfo.Name())
					err = os.Remove(filepath.Join(c.Dir, "pid", fileInfo.Name()))
				} else if c.KillOldPid {
					accessLogger.Printf("Killing existing bazel remote process: %d", pid)
					err = bazelRemoteProcess.Signal(syscall.Signal(syscall.SIGKILL))
				}
			}
		}
		return err
	})
	// create a file with pid number as the name and write critical server info into the file
	pidFile := filepath.Join(c.Dir, "pid", strconv.Itoa(os.Getpid()))
	port := []byte("port: " + strconv.Itoa(c.Port) + "\n")
	err = ioutil.WriteFile(pidFile, port, 0744)
	return err
}
