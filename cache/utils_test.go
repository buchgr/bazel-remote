package cache

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"testing"
)

func createRandomFile(dir string, size int64) string {
	data, filename := randomDataAndHash(size)
	filepath := dir + "/" + filename

	ioutil.WriteFile(filepath, data, 0744)
	return filename
}

func randomDataAndHash(size int64) ([]byte, string) {
	data := make([]byte, size)
	rand.Read(data)
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])
	return data, hashStr
}

func createTmpDir(t *testing.T) string {
	path, err := ioutil.TempDir("", "ensurespacer")
	if err != nil {
		t.Error("Couldn't create tmp dir", err)
	}
	return path
}
