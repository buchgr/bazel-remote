package main

import (
	"github.com/buchgr/bazelremote/cache"
)

// TODO: Add command line flags

func main() {
	e := cache.NewEnsureSpacer(0.8, 0.5)
	h := cache.NewHTTPCache(":8080", "/Users/buchgr/cache", 10*1024*1024, e)
	h.Serve()
}
