package disk

import (
	"fmt"
	"log"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/cache/disk/zstdimpl"
	"github.com/buchgr/bazel-remote/utils/validate"

	"github.com/djherbis/atime"

	"github.com/prometheus/client_golang/prometheus"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// New returns a new instance of a filesystem-based cache rooted at `dir`,
// with a maximum size of `maxSizeBytes` bytes and `opts` Options set.
func New(dir string, maxSizeBytes int64, opts ...Option) (Cache, error) {

	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return nil, err
	}

	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}

	// Go defaults to a limit of 10,000 operating system threads.
	// We probably don't need half of those for file removals at
	// any given point in time, unless the disk/fs can't keep up.
	// I suppose it's better to slow down processing than to crash
	// when hitting the 10k limit or to run out of disk space.
	semaphoreWeight := int64(5000)

	if strings.HasPrefix(runtime.GOOS, "darwin") {
		// Mac seems to fail to create os threads when removing
		// lots of files, so allow fewer than linux.
		semaphoreWeight = 3000
	}
	log.Printf("Limiting concurrent file removals to %d\n", semaphoreWeight)

	zi, err := zstdimpl.Get("go")
	if err != nil {
		return nil, err
	}

	c := diskCache{
		dir: dir,

		// Not using config here, to avoid test import cycles.
		storageMode:      casblob.Zstandard,
		zstd:             zi,
		maxBlobSize:      math.MaxInt64,
		maxProxyBlobSize: math.MaxInt64,

		fileRemovalSem: semaphore.NewWeighted(semaphoreWeight),

		gaugeCacheAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bazel_remote_disk_cache_longest_item_idle_time_seconds",
			Help: "The idle time (now - atime) of the last item in the LRU cache, updated once per minute. Depending on filesystem mount options (e.g. relatime), the resolution may be measured in 'days' and not accurate to the second. If using noatime this will be 0.",
		}),
	}

	cc := CacheConfig{diskCache: &c}

	// Apply options.
	for _, o := range opts {
		err = o(&cc)
		if err != nil {
			return nil, err
		}
	}

	// Create the directory structure.
	hexLetters := []byte("0123456789abcdef")
	for _, c1 := range hexLetters {
		for _, c2 := range hexLetters {
			subDir := string(c1) + string(c2)
			err := os.MkdirAll(filepath.Join(dir, cache.CAS.DirName(), subDir), os.ModePerm)
			if err != nil {
				return nil, err
			}
			err = os.MkdirAll(filepath.Join(dir, cache.AC.DirName(), subDir), os.ModePerm)
			if err != nil {
				return nil, err
			}
			err = os.MkdirAll(filepath.Join(dir, cache.RAW.DirName(), subDir), os.ModePerm)
			if err != nil {
				return nil, err
			}
		}
	}

	err = c.migrateDirectories()
	if err != nil {
		return nil, fmt.Errorf("Attempting to migrate the old directory structure failed: %w", err)
	}
	err = c.loadExistingFiles(maxSizeBytes)
	if err != nil {
		return nil, fmt.Errorf("Loading of existing cache entries failed due to error: %w", err)
	}

	if cc.metrics == nil {
		return &c, nil
	}

	cc.metrics.diskCache = &c

	return cc.metrics, nil
}

func (c *diskCache) migrateDirectories() error {
	err := migrateDirectory(c.dir, cache.AC)
	if err != nil {
		return err
	}
	err = migrateDirectory(c.dir, cache.CAS)
	if err != nil {
		return err
	}
	err = migrateDirectory(c.dir, cache.RAW)
	if err != nil {
		return err
	}
	return nil
}

func migrateDirectory(baseDir string, kind cache.EntryKind) error {
	sourceDir := path.Join(baseDir, kind.String())

	_, err := os.Stat(sourceDir)
	if os.IsNotExist(err) {
		return nil
	}

	log.Println("Migrating files (if any) to new directory structure:", sourceDir)

	listing, err := os.ReadDir(sourceDir)
	if err != nil {
		return err
	}

	// The v0 directory structure was lowercase sha256 hash filenames
	// stored directly in the ac/ and cas/ directories.

	// The v1 directory structure has subdirs for each two lowercase
	// hex character pairs.
	v1DirRegex := regexp.MustCompile("^[a-f0-9]{2}$")

	targetDir := path.Join(baseDir, kind.DirName())

	itemChan := make(chan os.DirEntry)
	errChan := make(chan error)

	var wg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for item := range itemChan {

				oldName := item.Name()
				oldNamePath := filepath.Join(sourceDir, oldName)

				if item.IsDir() {
					if !v1DirRegex.MatchString(oldName) {
						// Warn about non-v1 subdirectories.
						log.Println("Warning: unexpected directory", oldNamePath)
					}

					destDir := filepath.Join(targetDir, oldName[:2])
					err := migrateV1Subdir(oldNamePath, destDir, kind)
					if err != nil {
						log.Printf("Warning: failed to read subdir %q: %s",
							oldNamePath, err)
						continue
					}

					continue
				}

				if !item.Type().IsRegular() {
					log.Println("Warning: skipping non-regular file:", oldNamePath)
					continue
				}

				if !validate.HashKeyRegex.MatchString(oldName) {
					log.Println("Warning: skipping unexpected file:", oldNamePath)
					continue
				}

				src := filepath.Join(sourceDir, oldName)

				// Add a not-so-random "random" string. This should be OK
				// since there is probably only be one file for this hash.
				dest := filepath.Join(targetDir, oldName[:2], oldName+"-222444666")
				if kind == cache.CAS {
					dest += ".v1"
				}

				// TODO: make this work across filesystems?
				err := os.Rename(src, dest)
				if err != nil {
					errChan <- err
					return
				}
			}
		}()
	}

	err = nil
	numItems := len(listing)
	i := 1
	for _, item := range listing {
		select {
		case itemChan <- item:
			log.Printf("Migrating %s item(s) %d/%d, %s\n", sourceDir, i, numItems, item.Name())
			i++
		case err = <-errChan:
			log.Println("Encountered error while migrating files:", err)
			close(itemChan)
		}
	}
	close(itemChan)
	wg.Wait()

	if err != nil {
		return err
	}

	// Remove the empty directories.
	return os.RemoveAll(sourceDir)
}

func migrateV1Subdir(oldDir string, destDir string, kind cache.EntryKind) error {
	listing, err := os.ReadDir(oldDir)
	if err != nil {
		return err
	}

	if kind == cache.CAS {
		for _, item := range listing {

			oldPath := path.Join(oldDir, item.Name())

			if !validate.HashKeyRegex.MatchString(item.Name()) {
				return fmt.Errorf("Unexpected file: %s", oldPath)
			}

			destPath := path.Join(destDir, item.Name()) + "-556677.v1"
			err = os.Rename(oldPath, destPath)
			if err != nil {
				return fmt.Errorf("Failed to migrate CAS blob %s: %w",
					oldPath, err)
			}
		}

		return os.Remove(oldDir)
	}

	for _, item := range listing {
		oldPath := path.Join(oldDir, item.Name())

		if !validate.HashKeyRegex.MatchString(item.Name()) {
			return fmt.Errorf("Unexpected file: %s %s", oldPath, item.Name())
		}

		destPath := path.Join(destDir, item.Name()) + "-112233"

		// TODO: support cross-filesystem migration.
		err = os.Rename(oldPath, destPath)
		if err != nil {
			return fmt.Errorf("Failed to migrate blob %s: %w", oldPath, err)
		}
	}

	return nil
}

// Metadata for an lruItem.
type keyAndAtime struct {
	lookupKey string

	// atime of the file.
	ts time.Time
}

// The result of scanning a directory for cache items. This uses slices
// of pointers because it seems to perform better than slices of values.
// I suspect this might be due to a reduction of the amount of data to append.
type scanResult struct {
	// These will eventually populate the LRU index.
	item []*lruItem

	// Metadata for each item above. Both slices must be the same length.
	metadata []*keyAndAtime
}

// Implement the sort.Sort interface.
func (r scanResult) Len() int {
	return len(r.item)
}

func (r scanResult) Less(i, j int) bool {
	return r.metadata[i].ts.Before(r.metadata[j].ts)
}

func (r scanResult) Swap(i, j int) {
	r.item[i], r.item[j] = r.item[j], r.item[i]
	r.metadata[i], r.metadata[j] = r.metadata[j], r.metadata[i]
}

func (c *diskCache) scanDir() (scanResult, error) {

	numWorkers := runtime.NumCPU()
	if numWorkers < 4 {
		numWorkers = 4
	} else if numWorkers > 16 {
		numWorkers = 16 // Consider increasing the upper limit after more testing.
	}
	log.Println("Scanning cache directory with", numWorkers, "goroutines")

	dc := make(chan string, numWorkers) // Feed directory names to workers.
	dcClosed := false
	defer func() {
		if !dcClosed {
			close(dc)
		}
	}()

	scanResults := make(chan scanResult, numWorkers) // Received from workers.
	scanResultsClosed := false
	defer func() {
		if !scanResultsClosed {
			close(scanResults)
		}
	}()

	finalScanResult := scanResult{
		item:     []*lruItem{},
		metadata: []*keyAndAtime{},
	}

	received := make(chan struct{})

	go func() {
		for sr := range scanResults {
			finalScanResult.item = append(finalScanResult.item, sr.item...)
			finalScanResult.metadata = append(finalScanResult.metadata, sr.metadata...)
		}
		received <- struct{}{}
	}()

	dirListers := new(errgroup.Group)

	// compressed CAS items: <hash>-<logical size>-<random digits/ascii letters>
	// uncompressed CAS items: <hash>-<logical size>-<random digits/ascii letters>.v1
	// AC and RAW items: <hash>-<random digits/ascii letters>
	re := regexp.MustCompile(`^([a-f0-9]{64})(?:-([1-9][0-9]*))?-([0-9a-zA-Z]+)(\.v1)?$`)

	// Ignore lost+found dirs, which are automatically created in the
	// root dir of some unix style filesystems.
	const lostAndFound = "lost+found"

	for i := 0; i < numWorkers; i++ {
		dirListers.Go(func() error {
			for d := range dc {
				dirName := path.Join(c.dir, d)

				des, err := os.ReadDir(dirName)
				if err != nil {
					return err
				}

				item := make([]*lruItem, len(des))
				item_values := make([]lruItem, len(des))
				metadata := make([]*keyAndAtime, len(des))
				metadata_values := make([]keyAndAtime, len(des))

				n := 0 // The number of items to return for this dir.
				for _, de := range des {
					if de.IsDir() {
						if de.Name() == lostAndFound {
							continue
						}

						return fmt.Errorf("Unexpected directory: %s", de.Name())
					}

					info, err := de.Info()
					if err != nil {
						return fmt.Errorf("Failed to get file info: %w", err)
					}

					name := de.Name()

					fields := strings.Split(name, "/")
					file := fields[len(fields)-1]

					sm := re.FindStringSubmatch(file)
					if len(sm) != 5 {
						return fmt.Errorf("Unrecognized file: %q", name)
					}

					hash := sm[1]

					item[n] = &item_values[n]
					metadata[n] = &metadata_values[n]

					item[n].sizeOnDisk = info.Size()
					item[n].size = item[n].sizeOnDisk
					if len(sm[2]) > 0 {
						item[n].size, err = strconv.ParseInt(sm[2], 10, 64)
						if err != nil {
							return fmt.Errorf("Failed to parse int from %q: %w", sm[2], err)
						}
					}

					item[n].random = sm[3]
					if len(item[n].random) == 0 {
						return fmt.Errorf("Unrecognized file (no random string): %q", file)
					}

					item[n].legacy = sm[4] == ".v1"

					if strings.HasPrefix(d, "cas.v2/") {
						metadata[n].lookupKey = "cas/" + hash
					} else if strings.HasPrefix(d, "ac.v2/") {
						metadata[n].lookupKey = "ac/" + hash
					} else if strings.HasPrefix(d, "raw.v2/") {
						metadata[n].lookupKey = "raw/" + hash
					} else {
						return fmt.Errorf("Unrecognised file in cache dir: %q", d)
					}

					metadata[n].ts = atime.Get(info)

					n++
				}

				scanResults <- scanResult{
					item:     item[:n],
					metadata: metadata[:n],
				}
			}

			return nil
		})
	}

	des, err := os.ReadDir(c.dir)
	if err != nil {
		return scanResult{}, fmt.Errorf("Failed to read cache dir %q: %w", c.dir, err)
	}

	dre := regexp.MustCompile(`^[a-f0-9]{2}$`)

	for _, de := range des {
		name := de.Name()

		if !de.IsDir() {
			return scanResult{}, fmt.Errorf("Unexpected file: %s", name)
		}

		if name == lostAndFound {
			continue
		}

		if name != "ac.v2" && name != "cas.v2" && name != "raw.v2" {
			return scanResult{}, fmt.Errorf("Unexpected dir: %s", name)
		}

		dir := path.Join(c.dir, name)
		des2, err := os.ReadDir(dir)
		if err != nil {
			return scanResult{}, err
		}

		for _, de2 := range des2 {
			name2 := de2.Name()

			dirPath := path.Join(name, name2)

			if !de2.IsDir() {
				return scanResult{}, fmt.Errorf("Unexpected file: %s", dirPath)
			}

			if name2 == lostAndFound {
				continue
			}

			if !dre.MatchString(name2) {
				return scanResult{}, fmt.Errorf("Unexpected dir: %s", dirPath)
			}

			dc <- dirPath
		}
	}

	close(dc) // Ensure that the workers exit their range loop.
	dcClosed = true

	err = dirListers.Wait()
	if err != nil {
		return scanResult{}, err
	}
	close(scanResults)
	scanResultsClosed = true

	<-received

	return finalScanResult, nil
}

// loadExistingFiles lists all files in the cache directory, and adds them to the
// LRU index so that they can be served. Files are sorted by access time first,
// so that the eviction behavior is preserved across server restarts.
func (c *diskCache) loadExistingFiles(maxSizeBytes int64) error {
	log.Printf("Loading existing files in %s.\n", c.dir)

	result, err := c.scanDir()
	if err != nil {
		log.Printf("Failed to scan cache dir: %s", err.Error())
		return err
	}

	log.Println("Sorting cache files by atime.")
	sort.Sort(result)

	// The eviction callback deletes the file from disk.
	// This function is only called while the lock is held
	// by the current goroutine.
	onEvict := func(key Key, value lruItem) {
		f := c.getElementPath(key, value)
		// Run in a goroutine so we can release the lock sooner.
		go c.removeFile(f)
	}

	log.Println("Building LRU index.")

	c.lru = NewSizedLRU(maxSizeBytes, onEvict, len(result.item))

	for i := 0; i < len(result.item); i++ {
		ok := c.lru.Add(result.metadata[i].lookupKey, *result.item[i])
		if !ok {
			err = os.Remove(filepath.Join(c.dir, result.metadata[i].lookupKey))
			if err != nil {
				return err
			}
		}
	}

	log.Println("Finished loading disk cache files.")

	return nil
}
