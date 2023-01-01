package backendproxy

import (
	"io"

	"github.com/buchgr/bazel-remote/cache"
)

type UploadReq struct {
	Hash        string
	LogicalSize int64
	SizeOnDisk  int64
	Kind        cache.EntryKind
	Rc          io.ReadCloser
}

type Uploader interface {
	UploadFile(item UploadReq)
}

func StartUploaders(u Uploader, numUploaders int, maxQueuedUploads int) chan UploadReq {
	if maxQueuedUploads <= 0 || numUploaders <= 0 {
		return nil
	}

	uploadQueue := make(chan UploadReq, maxQueuedUploads)

	for i := 0; i < numUploaders; i++ {
		go func() {
			for item := range uploadQueue {
				u.UploadFile(item)
			}
		}()
	}

	return uploadQueue
}
