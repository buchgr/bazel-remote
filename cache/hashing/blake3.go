package hashing

import (
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"regexp"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	"lukechampine.com/blake3"
)

func init() {
	hasher := &b3Hasher{}
	register(hasher)
}

var b3Regex = regexp.MustCompile("^[a-f0-9]{64}$")

type b3Hasher struct{}

func (d *b3Hasher) New() hash.Hash {
	return blake3.New(d.Size(), nil)
}

func (d *b3Hasher) Hash(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (d *b3Hasher) DigestFunction() pb.DigestFunction_Value {
	return pb.DigestFunction_BLAKE3
}

func (d *b3Hasher) Dir() string {
	return "blake3"
}

func (d *b3Hasher) Empty() string {
	return "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"
}

func (d *b3Hasher) Size() int {
	return 32
}

func (d *b3Hasher) Validate(value string) error {
	if d.Size()*2 != len(value) {
		return fmt.Errorf("Invalid blake3 hash length %d: expected %d", len(value), d.Size())
	}
	if !b3Regex.MatchString(value) {
		return errors.New("Malformed blake3 hash " + value)
	}
	return nil
}

func (d *b3Hasher) ValidateDigest(hash string, size int64) error {
	if size == int64(0) {
		if hash == d.Empty() {
			return nil
		}
		return fmt.Errorf("Invalid zero-length %s hash", d.DigestFunction())
	}
	return d.Validate(hash)
}
