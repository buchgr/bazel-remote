package cache

import (
	"fmt"
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
	e.purge(cache, deltaBytes)

	e.mux.Lock()
	e.isPurging = false
	e.mux.Unlock()

	return cache.CurrSize()+addBytes <= cache.MaxSize()
}

func (e *ensureSpacer) purge(cache Cache, deltaBytes int64) int64 {
	d, err := os.Open(cache.Dir())
	if err != nil {
		log.Print(err)
		return 0
	}
	files, err := d.Readdir(-1)
	if err != nil {
		log.Print(err)
		return 0
	}
	sort.Slice(files, func(i, j int) bool {
		return atime.Get(files[i]).Before(atime.Get(files[j]))
	})
	var purgedBytes int64
	for _, fileinfo := range files {
		name := fileinfo.Name()
		path := fmt.Sprintf("%s%c%s", cache.Dir(), os.PathSeparator, name)
		err := os.Remove(path)
		if err == nil {
			cache.RemoveFile(filepath.Base(name))
		} else {
			log.Print(err)
		}
		if purgedBytes >= deltaBytes {
			break
		}
	}
	return purgedBytes
}
