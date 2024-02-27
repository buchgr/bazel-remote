package hashing

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"regexp"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
)

func init() {
	hasher := &sha256Hasher{}
	register(hasher)
}

var sha256Regex = regexp.MustCompile("^[a-f0-9]{64}$")

type sha256Hasher struct{}

func (d *sha256Hasher) New() hash.Hash {
	return sha256.New()
}

func (d *sha256Hasher) Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (d *sha256Hasher) DigestFunction() pb.DigestFunction_Value {
	return pb.DigestFunction_SHA256
}

func (d *sha256Hasher) Dir() string {
	return ""
}

func (d *sha256Hasher) Empty() string {
	return "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
}

func (d *sha256Hasher) Size() int {
	return sha256.Size
}

func (d *sha256Hasher) Validate(value string) error {
	if d.Size()*2 != len(value) {
		return fmt.Errorf("Invalid sha256 hash length %d: expected %d", len(value), d.Size())
	}
	if !sha256Regex.MatchString(value) {
		return errors.New("Malformed sha256 hash " + value)
	}
	return nil
}

func (d *sha256Hasher) ValidateDigest(hash string, size int64) error {
	if size == int64(0) {
		if hash == d.Empty() {
			return nil
		}
		return fmt.Errorf("Invalid zero-length %s hash", d.DigestFunction())
	}
	return d.Validate(hash)
}
