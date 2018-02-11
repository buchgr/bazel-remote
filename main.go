package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"

	"github.com/buchgr/bazel-remote/cache"
        auth "github.com/abbot/go-http-auth"
)

func main() {
	host := flag.String("host", "", "Host to bind the http server")
	port := flag.Int("port", 8080, "The port the HTTP server listens on")
	dir := flag.String("dir", "",
		"Directory path where to store the cache contents")
	maxSize := flag.Int64("max_size", -1,
		"The maximum size of the remote cache in GiB")
        htpasswd_file := flag.String("htpasswd_file", "", "Path to a .htpasswd file")
	flag.Parse()

	if *dir == "" || *maxSize <= 0 {
		flag.Usage()
		return
	}

	e := cache.NewEnsureSpacer(0.95, 0.5)
	h := cache.NewHTTPCache(*dir, *maxSize*1024*1024*1024, e)
	s := &http.Server{
		Addr:    *host + ":" + strconv.Itoa(*port),
		Handler: http.HandlerFunc(maybeAuth(h.CacheHandler, *htpasswd_file, *host)),
	}
	log.Fatal(s.ListenAndServe())
}

func maybeAuth(fn http.HandlerFunc, htpasswd_file string, host string) http.HandlerFunc {
       if htpasswd_file != "" {
         secrets := auth.HtpasswdFileProvider(htpasswd_file)
         authenticator := auth.NewBasicAuthenticator(host, secrets)
         return auth.JustCheck(authenticator, fn)
       }
       return fn;
}

