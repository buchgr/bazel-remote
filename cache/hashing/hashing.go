package hashing

import (
	"fmt"
	"hash"
	"strings"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
)

const DefaultFn = pb.DigestFunction_SHA256
const LegacyFn = pb.DigestFunction_SHA256

var DefaultHasher Hasher
var LegacyHasher Hasher

var registry map[pb.DigestFunction_Value]Hasher
var dfs []pb.DigestFunction_Value
var hashers []Hasher

func register(hasher Hasher) {
	if hasher.DigestFunction() == DefaultFn {
		DefaultHasher = hasher
	}
	if hasher.DigestFunction() == LegacyFn {
		LegacyHasher = hasher
	}
	if registry == nil {
		registry = make(map[pb.DigestFunction_Value]Hasher)
	}
	registry[hasher.DigestFunction()] = hasher
	dfs = append(dfs, hasher.DigestFunction())
	hashers = append(hashers, hasher)
}

func Supported(df pb.DigestFunction_Value) bool {
	_, ok := registry[df]
	return ok
}

func DigestFunctions() []pb.DigestFunction_Value {
	return dfs
}

func Hashers() []Hasher {
	return hashers
}

func DigestFunction(dfn string) pb.DigestFunction_Value {
	return pb.DigestFunction_Value(pb.DigestFunction_Value_value[strings.ToUpper(dfn)])
}

func Get(df pb.DigestFunction_Value) (Hasher, error) {
	if f, ok := registry[df]; ok {
		return f, nil
	}
	return nil, fmt.Errorf("no hash implementation for %s", df)
}

type Hasher interface {
	DigestFunction() pb.DigestFunction_Value
	New() hash.Hash
	Hash([]byte) string
	Dir() string
	Empty() string
	Size() int
	Validate(string) error
	ValidateDigest(string, int64) error
}
