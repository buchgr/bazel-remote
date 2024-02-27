package s3proxy

import (
	"path"
	"testing"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/hashing"
)

func TestObjectKey(t *testing.T) {
	testCases := []struct {
		prefix     string
		key        string
		kind       cache.EntryKind
		expectedV1 string
		expectedV2 string
	}{
		{"", "1234", cache.CAS, path.Join("cas", hashing.DefaultHasher.Dir(), "12/1234"), path.Join("cas.v2", hashing.DefaultHasher.Dir(), "12/1234")},
		{"test", "1234", cache.CAS, path.Join("test/cas", hashing.DefaultHasher.Dir(), "12/1234"), path.Join("test/cas.v2", hashing.DefaultHasher.Dir(), "12/1234")},
		{"foo/bar/grok", "1234", cache.CAS, path.Join("foo/bar/grok/cas", hashing.DefaultHasher.Dir(), "12/1234"), path.Join("foo/bar/grok/cas.v2", hashing.DefaultHasher.Dir(), "12/1234")},
		{"", "1234", cache.AC, path.Join("ac", hashing.DefaultHasher.Dir(), "12/1234"), path.Join("ac", hashing.DefaultHasher.Dir(), "12/1234")},
		{"", "1234", cache.RAW, path.Join("raw", hashing.DefaultHasher.Dir(), "12/1234"), path.Join("raw", hashing.DefaultHasher.Dir(), "12/1234")},
		{"foo/bar", "1234", cache.AC, path.Join("foo/bar/ac", hashing.DefaultHasher.Dir(), "12/1234"), path.Join("foo/bar/ac", hashing.DefaultHasher.Dir(), "12/1234")},
	}

	for _, tc := range testCases {
		result := objectKeyV2(tc.prefix, hashing.DefaultHasher, tc.key, tc.kind)
		if result != tc.expectedV2 {
			t.Errorf("objectKeyV2 did not match. (result: '%s' expected: '%s'",
				result, tc.expectedV2)
		}

		result = objectKeyV1(tc.prefix, hashing.DefaultHasher, tc.key, tc.kind)
		if result != tc.expectedV1 {
			t.Errorf("objectKeyV1 did not match. (result: '%s' expected: '%s'",
				result, tc.expectedV1)
		}
	}
}
