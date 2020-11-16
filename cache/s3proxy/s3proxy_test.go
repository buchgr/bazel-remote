package s3proxy

import (
	"log"
	"testing"

	"github.com/buchgr/bazel-remote/cache"
)

func TestObjectKey(t *testing.T) {
	logger := &log.Logger{}
	s := &s3Cache{
		mcore:        nil,
		bucket:       "test",
		keyVersion:   1,
		uploadQueue:  make(chan uploadReq, 1),
		accessLogger: logger,
		errorLogger:  logger,
	}

	// New key format tests
	s.keyVersion = 2

	testCases := []struct {
		prefix   string
		key      string
		kind     cache.EntryKind
		expected string
	}{
		{"", "1234", cache.CAS, "cas.v2/12/1234"},
		{"test", "1234", cache.CAS, "test/cas.v2/12/1234"},
	}

	for _, tc := range testCases {
		s.prefix = tc.prefix
		result := s.objectKey(tc.key, tc.kind)
		if result != tc.expected {
			t.Errorf("objectKey did not match. (result: '%s' expected: '%s'",
				result, tc.expected)
		}
	}
}
