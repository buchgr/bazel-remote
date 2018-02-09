package main

import (
	"flag"
	"log"
	"net/http"
	"strconv"

	"github.com/buchgr/bazel-remote/cache"
)

func main() {
	host := flag.String("host", "", "Host to bind the http server")
	port := flag.Int("port", 8080, "The port the HTTP server listens on")
	dir := flag.String("dir", "",
		"Directory path where to store the cache contents")
	maxSize := flag.Int64("max_size", -1,
		"The maximum size of the remote cache in GiB")
	flag.Parse()

	if *maxSize <= 0 {
		flag.Usage()
		return
	}

	e := cache.NewEnsureSpacer(0.95, 0.5)
	h := cache.NewHTTPCache(*dir, *maxSize*1024*1024*1024, e)
	s := &http.Server{
		Addr:    *host + ":" + strconv.Itoa(*port),
		Handler: http.HandlerFunc(h.CacheHandler),
	}
	log.Fatal(s.ListenAndServe())
}
