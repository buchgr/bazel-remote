// This runs an in-memory s3 server, for use with .bazelci/system-test.sh

package main

import (
	"log"
	"net/http"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

func main() {
	addr := "127.0.0.1:9000"

	backend := s3mem.New()
	err := backend.CreateBucket("bazel-remote")
	if err != nil {
		log.Fatal(err)
	}
	faker := gofakes3.New(backend)
	log.Fatal(http.ListenAndServe(addr, faker.Server()))
}
