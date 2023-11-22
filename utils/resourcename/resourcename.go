package resourcename

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/cache/hashing"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
)

func getDigestFunction(hash string) pb.DigestFunction_Value {
	// The function should be inferred from the length of the hash,
	// according to the protocol specs, but since we only support sha256 among
	// the legacy protocols we can just assume it. Leaving this here as a hook
	for _, hasher := range hashing.Hashers() {
		if hasher.DigestFunction() > 7 {
			continue
		}
		if hasher.Size()*2 == len(hash) {
			return hasher.DigestFunction()
		}
	}
	return pb.DigestFunction_UNKNOWN
}

func parseResource(name string, fields []string, allowMetadata bool) (hashing.Hasher, string, int64, casblob.CompressionType, error) {
	foundBlobs := false
	foundCompressedBlobs := false
	var ct casblob.CompressionType = casblob.Identity
	var rem []string
	for i := range fields {
		if i >= len(fields) {
			break
		}

		if fields[i] == "blobs" {
			foundBlobs = true
			rem = fields[i+1:]
			ct = casblob.Identity
			break
		}

		if fields[i] == "compressed-blobs" {
			rem = fields[i+2:]
			foundCompressedBlobs = true
			if fields[i+1] != "zstd" {
				return nil, "", 0, casblob.Identity, fmt.Errorf("Unable to parse compressor in resource name: %s", name)
			}
			ct = casblob.Zstandard
			break
		}
	}

	if !foundBlobs && !foundCompressedBlobs {
		return nil, "", 0, ct, fmt.Errorf("Unable to parse resource name: %s", name)
	}

	var df pb.DigestFunction_Value
	var hash, sizeStr string
	if len(rem) < 2 {
		return nil, "", 0, casblob.Identity, fmt.Errorf("Invalid resource name: %s", name)
	}
	if len(rem) == 2 {
		// The fn should be inferred from the length according to the protocol
		// specs, but since we only support sha256 among the legacy protocols
		// we can just assume it
		df, hash, sizeStr = getDigestFunction(rem[0]), rem[0], rem[1]
	} else {
		// protocol is a bit ambiguous here because it could either be
		// {digest_function}/{hash}/{size}
		// Or:
		// {hash}/{size}/{optional_metadata}
		df = hashing.DigestFunction(rem[0])
		if hashing.Supported(df) {
			hash, sizeStr = rem[1], rem[2]
		} else {
			if !allowMetadata {
				return nil, "", 0, casblob.Identity, fmt.Errorf("Invalid resource name: %s", name)
			}
			df, hash, sizeStr = getDigestFunction(rem[0]), rem[0], rem[1]
		}
	}

	size, err := strconv.ParseInt(sizeStr, 10, 64)

	if err != nil {
		return nil, "", 0, casblob.Identity, fmt.Errorf("Invalid size: %s from %q", sizeStr, name)
	}
	if size < 0 {
		return nil, "", 0, casblob.Identity, fmt.Errorf("Invalid size (must be non-negative): %d from %q", size, name)
	}

	hasher, err := hashing.Get(df)
	if err != nil {
		return nil, "", 0, casblob.Identity, err
	}

	err = hasher.ValidateDigest(hash, size)
	if err != nil {
		return nil, "", 0, casblob.Identity, err
	}

	return hasher, hash, size, ct, nil
}

// Parse a ReadRequest.ResourceName, return the validated hash, size, compression type and an error.
func ParseReadResource(name string) (hashing.Hasher, string, int64, casblob.CompressionType, error) {
	// The resource name should be of the format:
	// [{instance_name}]/blobs/[{digest_function}/]{hash}/{size}
	// Or:
	// [{instance_name}]/compressed-blobs/{compressor}/[{digest_function}/]{uncompressed_hash}/{uncompressed_size}

	// Instance_name is ignored in this bytestream implementation, so don't
	// bother returning it. It is not allowed to contain "blobs" as a distinct
	// path segment.
	fields := strings.Split(name, "/")

	hasher, hash, size, ct, err := parseResource(name, fields, false)
	if err != nil {
		return nil, "", 0, casblob.Identity, status.Error(codes.InvalidArgument, err.Error())
	}
	return hasher, hash, size, ct, err
}

// Parse a WriteRequest.ResourceName, return the validated hash, size,
// compression type and an optional error.
func ParseWriteResource(name string) (hashing.Hasher, string, int64, casblob.CompressionType, error) {
	// req.ResourceName is of the form:
	// [{instance_name}/]uploads/{uuid}/blobs/{digest_function}/]{hash}/{size}[/{optionalmetadata}]
	// Or, for compressed blobs:
	// [{instance_name}/]uploads/{uuid}/compressed-blobs/{compressor}/{digest_function}/]{uncompressed_hash}/{uncompressed_size}[{/optional_metadata}]
	fields := strings.Split(name, "/")
	var rem []string = nil
	for i := range fields {
		if i+2 >= len(fields) {
			break
		}
		if fields[i] == "uploads" {
			if fields[i+2] == "blobs" || fields[i+2] == "compressed-blobs" {
				rem = fields[i+2:]
			}
			break
		}
	}

	hasher, hash, size, ct, err := parseResource(name, rem, true)
	if err != nil {
		return nil, "", 0, casblob.Identity, status.Error(codes.InvalidArgument, err.Error())
	}
	return hasher, hash, size, ct, err
}

func getResourceName(compressed bool, hasher hashing.Hasher, hash string, size int64) string {
	name := fmt.Sprintf("%s/%d", hash, size)
	if hasher.DigestFunction() > 7 {
		name = fmt.Sprintf("%s/%s", strings.ToLower(hasher.DigestFunction().String()), name)
	}
	prefix := "blobs"
	if compressed {
		prefix = "compressed-blobs/zstd"
	}
	return fmt.Sprintf("%s/%s", prefix, name)
}

func GetReadResourceName(instance string, compressed bool, hasher hashing.Hasher, hash string, size int64) string {
	return path.Join(instance, getResourceName(compressed, hasher, hash, size))
}

func GetWriteResourceName(instance string, compressed bool, hasher hashing.Hasher, hash string, size int64, metadata string) string {
	return path.Join(instance, "uploads", uuid.NewString(), getResourceName(compressed, hasher, hash, size), metadata)
}
