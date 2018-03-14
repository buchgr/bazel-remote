package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"

	auth "github.com/abbot/go-http-auth"
	"github.com/buchgr/bazel-remote/cache"
	"os"
)

func main() {
	host := flag.String("host", "", "Address to listen on. Listens on all network interfaces by default.")
	port := flag.Int("port", 8080, "The port the HTTP server listens on")
	dir := flag.String("dir", "",
		"Directory path where to store the cache contents. This flag is required.")
	maxSize := flag.Int64("max_size", -1,
		"The maximum size of the remote cache in GiB. This flag is required.")
	htpasswd_file := flag.String("htpasswd_file", "", "Path to a .htpasswd file. This flag is optional. Please read https://httpd.apache.org/docs/2.4/programs/htpasswd.html.")
	tls_enabled := flag.Bool("tls_enabled", false, "Bool specifying whether or not to start the server with tls.  If true, server_cert and server_key flags are requred.")
	tls_cert_file := flag.String("tls_cert_file", "", "Path to a PEM encoded certificate file.  Required if tls_enabled is set to true.")
	tls_key_file := flag.String("tls_key_file", "", "Path to a PEM encoded key file.  Required if tls_enabled is set to true.")

	flag.Parse()

	if *dir == "" || *maxSize <= 0 {
		flag.Usage()
		return
	}

	accessLogger := log.New(os.Stdout, "", log.Ldate | log.Ltime | log.LUTC)
	errorLogger := log.New(os.Stdout, "", log.Ldate | log.Ltime | log.LUTC)
	e := cache.NewEnsureSpacer(0.95, 0.5)
	h := cache.NewHTTPCache(*dir, *maxSize*1024*1024*1024, e, *accessLogger, *errorLogger)

	http.HandleFunc("/status", h.StatusPageHandler)
	http.HandleFunc("/", maybeAuth(h.CacheHandler, *htpasswd_file, *host))
	var serverErr error

	if *tls_enabled {
		if len(*tls_cert_file) < 1 || len(*tls_key_file) < 1 {
			flag.Usage()
			return
		}
		serverErr = http.ListenAndServeTLS(*host+":"+strconv.Itoa(*port), *tls_cert_file, *tls_key_file, nil)
	} else {
		serverErr = http.ListenAndServe(*host+":"+strconv.Itoa(*port), nil)
	}
	if serverErr != nil {
		log.Fatal("ListenAndServe: ", serverErr)
	}
}

func maybeAuth(fn http.HandlerFunc, htpasswd_file string, host string) http.HandlerFunc {
	if htpasswd_file != "" {
		secrets := auth.HtpasswdFileProvider(htpasswd_file)
		authenticator := auth.NewBasicAuthenticator(host, secrets)
		return auth.JustCheck(authenticator, fn)
	}
	return fn
}
