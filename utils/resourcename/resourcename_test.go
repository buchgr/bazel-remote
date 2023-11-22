package resourcename

import (
	"testing"

	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
)

func TestParseReadResource(t *testing.T) {
	t.Parallel()

	// Format: [{instance_name}]/blobs/{hash}/{size}

	tcs := []struct {
		resourceName        string
		expectedHash        string
		expectedSize        int64
		expectedCompression casblob.CompressionType
		expectedDigestFn    pb.DigestFunction_Value
		expectError         bool
	}{
		{
			// No instance specified.
			"blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			// No instance specified.
			"compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			// Instance specified.
			"foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			// Instance specified.
			"foo/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			// Instance specified, containing '/'.
			"foo/bar/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			// Instance specified, containing '/'.
			"foo/bar/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			// Missing "/blobs/" or "/compressed-blobs/".
			resourceName: "foo/bar/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},

		// Instance names cannot contain the following path segments: blobs,
		// uploads, actions, actionResults, operations or `capabilities. We
		// only care about "blobs".
		{
			resourceName: "blobs/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "blobs/foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},

		// Invalid hashes (we only support lowercase SHA256).
		{
			resourceName: "foo/blobs/blobs/01234567890123456789012345678901234567890123456789012345678901234/42",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/012345678901234567890123456789012345678901234567890123456789012/42",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/g123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/A123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true, // Must be lowercase.
		},
		{
			resourceName: "foo/blobs//42",
			expectError:  true,
		},

		// Invalid sizes (must be valid non-negative int64).
		{
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/-0",
			expectError:  true,
		},
		{
			// We use -1 as a placeholder for "size unknown" when validating AC digests, but it's invalid here.
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/-1",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/3.14",
			expectError:  true,
		},
		{
			// Size: max(int64) + 1
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/9223372036854775808",
			expectError:  true,
		},

		// Trailing garbage.
		{
			resourceName: "blobs/0123456789012345678901234567890123456789012345678901234567890123/42abc",
			expectError:  true,
		},
		{
			resourceName: "blobs/0123456789012345678901234567890123456789012345678901234567890123/42/abc",
			expectError:  true,
		},

		// Misc.
		{
			resourceName: "foo/blobs/a",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs//42",
			expectError:  true,
		},

		// Unsupported/unrecognised compression types.
		{
			resourceName: "pretenduuid/compressed-blobs/zstandard/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/Zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/ZSTD/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/identity/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/Identity/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/IDENTITY/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
	}

	for _, tc := range tcs {
		hasher, hash, size, cmp, err := ParseReadResource(tc.resourceName)

		if tc.expectError {
			if err == nil {
				t.Fatalf("Expected an error for %q, got nil and hash: %q size: %d", tc.resourceName, hash, size)
			}

			continue
		}

		if !tc.expectError && (err != nil) {
			t.Fatalf("Expected an success for %q, got error %q", tc.resourceName, err)
		}

		if tc.expectedDigestFn != hasher.DigestFunction() {
			t.Fatalf("Expected digest function: %q did not match actual digest function: %q in %q", tc.expectedDigestFn, hasher.DigestFunction(), tc.resourceName)
		}

		if hash != tc.expectedHash {
			t.Fatalf("Expected hash: %q did not match actual hash: %q in %q", tc.expectedHash, hash, tc.resourceName)
		}

		if size != tc.expectedSize {
			t.Fatalf("Expected size: %d did not match actual size: %d in %q", tc.expectedSize, size, tc.resourceName)
		}

		if cmp != tc.expectedCompression {
			t.Fatalf("Expected compressor: %d did not match actual compressor: %d in %q", tc.expectedCompression, cmp, tc.resourceName)
		}
	}
}

func TestParseWriteResource(t *testing.T) {
	t.Parallel()

	// Format: [{instance_name}/]uploads/{uuid}/blobs/{hash}/{size}[/{optionalmetadata}]
	// Or: [{instance_name}/]uploads/{uuid}/compressed-blobs/{compressor}/{uncompressed_hash}/{uncompressed_size}[{/optional_metadata}]

	// We ignore instance_name and metadata, and we don't verify that the
	// uuid is valid- it just needs to exist (or be empty) and not contain '/'.

	tcs := []struct {
		resourceName        string
		expectedHash        string
		expectedSize        int64
		expectedCompression casblob.CompressionType
		expectedDigestFn    pb.DigestFunction_Value
		expectError         bool
	}{
		{
			"foo/uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			"foo/uploads/pretenduuid/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			"uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			"uploads/pretenduuid/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			// max(int64)
			"uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/9223372036854775807",
			"0123456789012345678901234567890123456789012345678901234567890123",
			9223372036854775807,
			casblob.Identity,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			// max(int64)
			"uploads/pretenduuid/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/9223372036854775807",
			"0123456789012345678901234567890123456789012345678901234567890123",
			9223372036854775807,
			casblob.Zstandard,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			"foo/uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42/some/meta/data",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			pb.DigestFunction_SHA256,
			false,
		},
		{
			"foo/uploads/pretenduuid/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42/some/meta/data",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			pb.DigestFunction_SHA256,
			false,
		},

		// Missing "uploads"
		{
			resourceName: "/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},

		{
			// Missing uuid.
			resourceName: "uploads/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			// Multiple segments in place of uuid.
			resourceName: "uploads/uuid/with/segments/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},

		// Invalid hashes.
		{
			// Too long.
			resourceName: "uploads/pretenduuid/blobs/01234567890123456789012345678901234567890123456789012345678901234/42",
			expectError:  true,
		},
		{
			// Too short.
			resourceName: "uploads/pretenduuid/blobs/012345678901234567890123456789012345678901234567890123456789012/42",
			expectError:  true,
		},
		{
			// Not lowercase.
			resourceName: "uploads/pretenduuid/blobs/A123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/blobs//42", // Missing entirely.
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/blobs/42", // Missing entirely.
			expectError:  true,
		},

		// Invalid sizes (must be valid non-negative int64).
		{
			resourceName: "uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/-0",
			expectError:  true,
		},
		{
			// We use -1 as a placeholder for "size unknown" when validating AC digests, but it's invalid here.
			resourceName: "uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/-1",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/2.71828",
			expectError:  true,
		},
		{
			// Size: max(int64) + 1
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/9223372036854775808",
			expectError:  true,
		},

		// Unsupported/unrecognised compression types.
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/zstandard/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/Zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/ZSTD/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/identity/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/Identity/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/IDENTITY/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},

		// Contains digest function
		{
			resourceName: "uploads/pretenduuid/blobs/foo/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/zstd/foo/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
	}

	for _, tc := range tcs {
		hasher, hash, size, cmp, err := ParseWriteResource(tc.resourceName)

		if tc.expectError {
			if err == nil {
				t.Fatalf("Expected an error for %q, got nil and hash: %q size: %d", tc.resourceName, hash, size)
			}

			continue
		}

		if !tc.expectError && (err != nil) {
			t.Fatalf("Expected an success for %q, got error %q", tc.resourceName, err)
		}

		if tc.expectedDigestFn != hasher.DigestFunction() {
			t.Fatalf("Expected digest function: %q did not match actual digest function: %q in %q", tc.expectedDigestFn, hasher.DigestFunction(), tc.resourceName)
		}

		if hash != tc.expectedHash {
			t.Fatalf("Expected hash: %q did not match actual hash: %q in %q", tc.expectedHash, hash, tc.resourceName)
		}

		if size != tc.expectedSize {
			t.Fatalf("Expected size: %d did not match actual size: %d in %q", tc.expectedSize, size, tc.resourceName)
		}

		if cmp != tc.expectedCompression {
			t.Fatalf("Expected compression: %d did not match actual compression: %d in %q", tc.expectedCompression, cmp, tc.resourceName)
		}
	}
}
