package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"

	auth "github.com/abbot/go-http-auth"
	"github.com/buchgr/bazel-remote/cache"
)

func main() {
	host := flag.String("host", "", "Address to listen on. Listens on all network interfaces by default.")
	port := flag.Int("port", 8080, "The port the HTTP server listens on")
	dir := flag.String("dir", "",
		"Directory path where to store the cache contents. This flag is required.")
	maxSize := flag.Int64("max_size", -1,
		"The maximum size of the remote cache in GiB. This flag is required.")
	htpasswd_file := flag.String("htpasswd_file", "", "Path to a .htpasswd file. This flag is optional. Please read https://httpd.apache.org/docs/2.4/programs/htpasswd.html.")
	tls_enabled := flag.Bool("tls_enabled", false, "Bool specifying wheather or not to start the server with tls.  If true, default port changes to 8443, and server_cert and server_key flags are requred.")
	server_cert := flag.String("server_cert", "", "Path to a pem encoded certificate file.  Required if tls_enabled is set to true.")
	server_key := flag.String("server_key", "", "Path to a pem encoded key file.  Required if tls_enabled is set to true.")

	flag.Parse()

	if *dir == "" || *maxSize <= 0 {
		flag.Usage()
		return
	}

	e := cache.NewEnsureSpacer(0.95, 0.5)
	h := cache.NewHTTPCache(*dir, *maxSize*1024*1024*1024, e)

	http.HandleFunc("/", maybeAuth(h.CacheHandler, *htpasswd_file, *host))
	var serverErr error

	if *tls_enabled {
		if len(*server_cert) < 1 || len(*server_key) < 1 {
			flag.Usage()
			return
		}
		if *port == 8080 {
			*port = 8443
		}
		serverErr = http.ListenAndServeTLS(*host+":"+strconv.Itoa(*port), *server_cert, *server_key, nil)
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
