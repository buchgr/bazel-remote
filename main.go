package main

import (
	"log"
	"net/http"
	"strconv"

	auth "github.com/abbot/go-http-auth"
	"github.com/buchgr/bazel-remote/cache"
	"github.com/urfave/cli"
	"os"
)

func main() {

	app := cli.NewApp()
	app.Description = "A remote build cache for Bazel."
	app.Usage = "A remote build cache for Bazel"
	app.HideHelp = true
	app.HideVersion = true

	app.Flags = []cli.Flag{
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
			Name:   "htpasswd_file",
			Value:  "",
			Usage:  "Path to a .htpasswd file. This flag is optional. Please read https://httpd.apache.org/docs/2.4/programs/htpasswd.html.",
			EnvVar: "BAZEL_REMOTE_HTPASSWD_FILE",
		},
		cli.BoolFlag{
			Name:   "tls_enabled",
			Usage:  "Bool specifying whether or not to start the server with tls. If true, server_cert and server_key flags are required.",
			EnvVar: "BAZEL_REMOTE_TLS_ENABLED",
		},
		cli.StringFlag{
			Name:   "tls_cert_file",
			Value:  "",
			Usage:  "Path to a pem encoded certificate file. Required if tls_enabled is set to true.",
			EnvVar: "BAZEL_REMOTE_TLS_CERT_FILE",
		},
		cli.StringFlag{
			Name:   "tls_key_file",
			Value:  "",
			Usage:  "Path to a pem encoded key file. Required if tls_enabled is set to true.",
			EnvVar: "BAZEL_REMOTE_TLS_KEY_FILE",
		},
	}

	app.Action = func(c *cli.Context) error {

		host := c.String("host")
		port := c.Int("port")
		dir := c.String("dir")
		maxSize := c.Int64("max_size")
		htpasswdFile := c.String("htpasswd_file")
		tlsEnabled := c.Bool("tls_enabled")
		tlsCertFile := c.String("tls_cert_file")
		tlsKeyFile := c.String("tls_key_file")

		if dir == "" || maxSize <= 0 {
			return cli.ShowAppHelp(c)
		}

		log.SetFlags(log.Ldate | log.Ltime | log.LUTC | log.Lshortfile)
		accessLogger := log.New(os.Stdout, "", log.Ldate|log.Ltime|log.LUTC)
		errorLogger := log.New(os.Stderr, "", log.Ldate|log.Ltime|log.LUTC)

		blobStore := cache.NewFsBlobStore(dir, maxSize*1024*1024*1024)

		h := cache.NewHTTPCache(blobStore, accessLogger, errorLogger)

		http.HandleFunc("/status", h.StatusPageHandler)
		http.HandleFunc("/", maybeAuth(h.CacheHandler, htpasswdFile, host))

		if tlsEnabled {
			if len(tlsCertFile) < 1 || len(tlsKeyFile) < 1 {
				return cli.ShowAppHelp(c)
			}
			return http.ListenAndServeTLS(host+":"+strconv.Itoa(port), tlsCertFile, tlsKeyFile, nil)
		}
		return http.ListenAndServe(host+":"+strconv.Itoa(port), nil)
	}

	serverErr := app.Run(os.Args)
	if serverErr != nil {
		log.Fatal("ListenAndServe: ", serverErr)
	}
}

func maybeAuth(fn http.HandlerFunc, htpasswdFile string, host string) http.HandlerFunc {
	if htpasswdFile != "" {
		secrets := auth.HtpasswdFileProvider(htpasswdFile)
		authenticator := auth.NewBasicAuthenticator(host, secrets)
		return auth.JustCheck(authenticator, fn)
	}
	return fn
}
