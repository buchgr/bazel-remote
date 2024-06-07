package hashing

import (
	"crypto/rand"
	"fmt"
	"math"
	"testing"
)

func BenchmarkHashers(b *testing.B) {
	prettySize := func(size int64) (int64, string) {
		for _, unit := range []string{"B", "KB", "MB", "GB"} {
			if size < 1024 {
				return size, unit
			}
			size /= 1024
		}
		return size, "GB"
	}

	for i := 0; i <= 30; i++ {
		nBytes := int64(math.Pow(2, float64(i)))
		data := make([]byte, nBytes)
		size, unit := prettySize(nBytes)
		_, err := rand.Read(data)
		if err != nil {
			b.Fatal(err)
		}

		for _, hasher := range Hashers() {
			b.Run(fmt.Sprintf("%d%s %s", size, unit, hasher.DigestFunction().String()), func(b *testing.B) {
				hasher.Hash(data)
			})
		}
	}
}
