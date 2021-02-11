package tempfile

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// Creator maintains the state of a pseudo-random number generator
// used to create temp files.
type Creator struct {
	mu   sync.Mutex
	idum uint32 // Pseudo-random number generator state.
}

// NewCreator returns a new Creator, for creating temp files.
func NewCreator() *Creator {
	return &Creator{idum: uint32(time.Now().UnixNano())}
}

// Fast "quick and dirty" linear congruential (pseudo-random) number
// generator from Numerical Recipes. Excerpt here:
// https://www.unf.edu/~cwinton/html/cop4300/s09/class.notes/LCGinfo.pdf
// This is the same algorithm as used in the ioutil.TempFile go standard
// library function.
func (c *Creator) ranqd1() string {
	c.mu.Lock()
	c.idum = c.idum*1664525 + 1013904223
	r := c.idum
	c.mu.Unlock()
	return strconv.Itoa(int(1e9 + r%1e9))[1:]
}

const flags = os.O_RDWR | os.O_CREATE | os.O_EXCL

const EndMode = 0666
const wipMode = EndMode | os.ModeSetgid

// Create attempts to create a file whose name is of the form
// <base>-<randomstring> and with a ".v1" suffix if `legacy` is
// true. The file will be created with the setgid bit set, which
// indicates that it is not complete. The *os.File is returned
// along with the random string, and an error if something went
// wrong.
//
// Once the file has been successfully written by the caller, it
// should be chmod'ed to `EndMode` to mark it as complete.
func (c *Creator) Create(base string, legacy bool) (*os.File, string, error) {
	var err error
	var f *os.File
	var name string
	var random string

	for i := 0; i < 10000; i++ {
		random = c.ranqd1()
		if legacy {
			name = base + "-" + random + ".v1"
		} else {
			name = base + "-" + random
		}

		f, err = os.OpenFile(name, flags, wipMode)
		if os.IsExist(err) {
			continue
		}
		return f, random, err
	}
	return nil, "", err
}
