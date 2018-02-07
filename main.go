package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"

	"github.com/bajacondor/bazel-remote/cache"
	//"github.com/buchgr/bazel-remote/cache"
)

func main() {
	host := flag.String("host", "", "Host to bind the http server")
	port := flag.Int("port", 8080, "The port the HTTP server listens on")
	dir := flag.String("dir", "",
		"Directory path where to store the cache contents")
	maxSize := flag.Int64("max_size", -1,
		"The maximum size of the remote cache in GiB")
	user := flag.String("user", "",
		"Username for basic authentication")
	pass := flag.String("pass", "",
		"Password for basic authentication")
	flag.Parse()

	if *maxSize <= 0 {
		flag.Usage()
		return
	}

	e := cache.NewEnsureSpacer(0.95, 0.5)
	h := cache.NewHTTPCache(*dir, *maxSize*1024*1024*1024, e)
	s := &http.Server{
		Addr:    *host + ":" + strconv.Itoa(*port),
		Handler: http.HandlerFunc(auth(h.CacheHandler, *user, *pass)),
	}
	log.Fatal(s.ListenAndServe())
}

func auth(fn http.HandlerFunc, eu, ep string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ep != "" {
			user, pass, _ := r.BasicAuth()
			if !check(user, pass, eu, ep) {
				http.Error(w, "Unauthorized.", 401)
				return
			}
		}
		fn(w, r)
	}
}

func check(u, p, eu, ep string) bool {
	return u == eu && p == ep
}
