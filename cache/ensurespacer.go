package cache

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/djherbis/atime"
)

// EnsureSpacer ...
type EnsureSpacer interface {
	EnsureSpace(cache Cache, addBytes int64) bool
}

type ensureSpacer struct {
	triggerPct float64
	targetPct  float64
	isPurging  bool
	mux        *sync.Mutex
}

// NewEnsureSpacer ...
func NewEnsureSpacer(triggerPct float64, targetPct float64) EnsureSpacer {
	return &ensureSpacer{triggerPct, targetPct, false, &sync.Mutex{}}
}

func (e *ensureSpacer) EnsureSpace(cache Cache, addBytes int64) bool {
	shouldPurge :=
		cache.CurrSize()+addBytes >= int64(float64(cache.MaxSize())*e.triggerPct)
	if !shouldPurge {
		// Fast Path
		return true
	}
	e.mux.Lock()
	shouldPurge =
		cache.CurrSize()+addBytes >= int64(float64(cache.MaxSize())*e.triggerPct)
	if !shouldPurge || e.isPurging {
		enoughSpace := cache.CurrSize()+addBytes <= cache.MaxSize()
		e.mux.Unlock()
		return enoughSpace
	}
	e.isPurging = true
	e.mux.Unlock()

	targetBytes := int64(float64(cache.MaxSize()) * e.targetPct)
	deltaBytes := cache.CurrSize() - targetBytes
	log.Printf("Removed %v bytes from cache\n", e.purge(cache, deltaBytes))

	e.mux.Lock()
	e.isPurging = false
	e.mux.Unlock()

	return cache.CurrSize()+addBytes <= cache.MaxSize()
}

func (e *ensureSpacer) purge(cache Cache, deltaBytes int64) int64 {
	type file struct {
		Path string
		Info os.FileInfo
	}
	files := make([]file, cache.NumFiles())[:0]
	err := filepath.Walk(
		cache.Dir(),
		func(path string, info os.FileInfo, err error) error {
			// Ignore any files that the cache does not know about. These are most
			// likely directories or ongoing uploads.
			if cache.ContainsFile(path) {
				files = append(files, file{path, info})
			}
			return nil
		})
	if err != nil {
		return 0
	}

	sort.Slice(files, func(i, j int) bool {
		return atime.Get(files[i].Info).Before(atime.Get(files[j].Info))
	})

	var purgedBytes int64
	for _, file := range files {
		if err := os.Remove(file.Path); err != nil {
			log.Printf("Could not remove %v: %v\n", file.Path, err)
		}

		purgedBytes += cache.RemoveFile(file.Path)
		if purgedBytes >= deltaBytes {
			return purgedBytes
		}
	}
	return purgedBytes
}
