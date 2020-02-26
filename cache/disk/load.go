package disk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"sync"

	"github.com/djherbis/atime"

	"github.com/buchgr/bazel-remote/cache"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
)

type scanDir struct {
	name    string
	dest    string
	version int
	kind    cache.EntryKind
}

// Return a list of importItems sorted by atime, and a boolean that is
// true if the caller should migrate items, in reverse LRU order.
func (c *DiskCache) findCacheItems() ([]importItem, bool, error) {
	files := []importItem{}

	var mu sync.Mutex // Protects the migrate variable below:
	migrate := false

	// Workers submit discovered files here.
	filesChan := make(chan []importItem)

	// Workers receive a dir to scan here.
	workChan := make(chan scanDir)

	// Workers can report errors here:
	errChan := make(chan error)

	numWorkers := runtime.NumCPU() // TODO: consider tweaking this.

	hashKeyRegex := regexp.MustCompile("^[a-f0-9]{64}$")

	// Spawn some worker goroutines to read the cache concurrently.
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(i int) {
			needMigration := false
			defer func() {
				if needMigration {
					mu.Lock()
					migrate = true
					mu.Unlock()
				}
				wg.Done()
			}()

			for scanDir := range workChan {
				_, err := os.Stat(scanDir.name)
				if os.IsNotExist(err) {
					continue
				} else if err != nil {
					errChan <- err
					return
				}

				listing, err := ioutil.ReadDir(scanDir.name)
				if err != nil {
					errChan <- err
					return
				}

				addChecksum := scanDir.version < 2 && (scanDir.kind != cache.CAS)

				toSend := make([]importItem, 0, len(listing))
				for e := range listing {
					if listing[e].IsDir() {
						continue
					}

					if !hashKeyRegex.MatchString(listing[e].Name()) {
						log.Println("Unexpected file in cache:",
							filepath.Join(scanDir.name, listing[e].Name()))
						continue
					}

					basename := listing[e].Name()
					entry := importItem{
						name:        filepath.Join(scanDir.name, basename),
						info:        listing[e],
						addChecksum: addChecksum,
					}

					if scanDir.version < 2 {
						entry.oldName = entry.name
						if scanDir.kind == cache.CAS {
							entry.name = filepath.Join(c.dir,
								scanDir.kind.String(),
								basename[:2],
								basename)
						} else {
							entry.name = filepath.Join(c.dir,
								scanDir.kind.String()+".v2",
								basename[:2],
								basename)
						}

						needMigration = true
					}

					toSend = append(toSend, entry)
				}

				if len(toSend) > 0 {
					filesChan <- toSend
				}
			}
		}(i)
	}

	go func() {
		wg.Wait()
		// All workers have now finished.
		close(filesChan)
	}()

	// Provide the workers with directories to scan.

	workChan <- scanDir{
		name:    filepath.Join(c.dir, "ac"),
		version: 0,
		kind:    cache.AC,
	}
	workChan <- scanDir{
		name:    filepath.Join(c.dir, "cas"),
		version: 0,
		kind:    cache.CAS,
	}

	hexLetters := []byte("0123456789abcdef")
	for _, c1 := range hexLetters {
		for _, c2 := range hexLetters {
			subDir := string(c1) + string(c2)

			workChan <- scanDir{
				name:    filepath.Join(c.dir, "cas", subDir),
				version: 2, // v1 and v2 cas dirs are the same.
				kind:    cache.CAS,
			}

			workChan <- scanDir{
				name:    filepath.Join(c.dir, "ac", subDir),
				version: 1,
				kind:    cache.AC,
			}
			workChan <- scanDir{
				name:    filepath.Join(c.dir, "ac.v2", subDir),
				version: 2,
				kind:    cache.AC,
			}

			workChan <- scanDir{
				name:    filepath.Join(c.dir, "raw", subDir),
				version: 1,
				kind:    cache.RAW,
			}
			workChan <- scanDir{
				name:    filepath.Join(c.dir, "raw.v2", subDir),
				version: 2,
				kind:    cache.RAW,
			}
		}
	}

	// No more dirs for the workers to process.
	close(workChan)

OuterLoop:
	for {
		select {
		case err := <-errChan:
			return nil, false, err
		case f, found := <-filesChan:
			if !found {
				break OuterLoop
			}
			files = append(files, f...)
		}
	}

	log.Println("Sorting cache files by atime.")
	sort.Slice(files, func(i int, j int) bool {
		return atime.Get(files[i].info).Before(atime.Get(files[j].info))
	})

	return files, migrate, nil
}

func updateAccesstime(file string) {
	f, err := os.Open(file)
	if err != nil {
		return
	}
	var buf [1]byte
	f.Read(buf[:])
	f.Close()
}

func (c *DiskCache) migrateFiles(files []importItem) error {
	log.Println("Migrating old cache items to new directory structure.")

	var err error
	for _, i := range files {
		if i.oldName == "" {
			updateAccesstime(filepath.Join(c.dir, i.name))
			continue
		}

		if !i.addChecksum {
			err = os.Rename(i.oldName, i.name)
			if err != nil {
				return err
			}

			continue
		}

		err = moveAndChecksum(i.oldName, i.name)
		if err != nil {
			return err
		}
	}

	// Try to remove old (hopefully) empty dirs.

	hexLetters := []byte("0123456789abcdef")
	for _, c1 := range hexLetters {
		for _, c2 := range hexLetters {
			subDir := string(c1) + string(c2)

			acV1subDir := filepath.Join(c.dir, "ac", subDir)
			err := os.Remove(acV1subDir)
			if err != nil && !os.IsNotExist(err) {
				log.Printf("Warning: failed to remove old format directory \"%s\": %v",
					acV1subDir, err)
			}
			rawV1subDir := filepath.Join(c.dir, "raw", subDir)
			err = os.Remove(rawV1subDir)
			if err != nil && !os.IsNotExist(err) {
				log.Printf("Warning: failed to remove old format directory \"%s\": %v",
					acV1subDir, err)
			}
		}
	}

	acV1dir := filepath.Join(c.dir, "ac")
	err = os.Remove(acV1dir)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: failed to remove old format directory \"%s\": %v",
			acV1dir, err)
	}
	rawV1dir := filepath.Join(c.dir, "raw")
	err = os.Remove(rawV1dir)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: failed to remove old format directory \"%s\": %v",
			rawV1dir, err)
	}

	return nil
}

// Replace a raw file with a "v2" style file with the data integrity
// header. `old` and `new` must be different files (OK since we store
// v2 style files in different directories.
func moveAndChecksum(old string, new string) error {

	key := filepath.Base(old)
	dt := digestType(key)
	if dt == pb.DigestFunction_UNKNOWN {
		return fmt.Errorf("Unsupported digest: %s", old)
	}

	headerSize, ok := headerSize[dt]
	if !ok {
		return fmt.Errorf("Unknown header size for digest: %d", dt)
	}

	success := false
	openOld := false
	openNew := false
	var in *os.File
	var out *os.File

	defer func() {
		if openOld {
			in.Close()
		}

		if openNew {
			out.Close()

			if !success {
				os.Remove(new)
			}
		}

		if success {
			os.Remove(old)
		}
	}()

	in, err := os.Open(old)
	if err != nil {
		return err
	}
	openOld = true

	out, err = os.Create(new)
	if err != nil {
		return err
	}
	openNew = true

	// Make space for the header.
	_, err = out.Seek(headerSize, 0)
	if err != nil {
		return err
	}

	hasher := sha256.New()
	mw := io.MultiWriter(out, hasher)

	sizeBytes, err := io.Copy(mw, in)
	if err != nil {
		return err
	}

	// Go back and fill in the header.
	_, err = out.Seek(0, 0)
	if err != nil {
		return err
	}

	hashBytes := hasher.Sum(nil)
	hashStr := hex.EncodeToString(hashBytes)

	err = writeHeader(out, hashStr, sizeBytes)
	if err != nil {
		return err
	}

	success = true

	return nil
}
