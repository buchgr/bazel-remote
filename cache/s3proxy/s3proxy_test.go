package s3proxy

import (
	"log"
	"testing"

	"github.com/buchgr/bazel-remote/cache"
)

func TestObjectKey(t *testing.T) {
	result, expected := "", ""
	logger := &log.Logger{}
	tc := &s3Cache{
		mcore:        nil,
		bucket:       "test",
		newKeyFormat: false,
		uploadQueue:  make(chan uploadReq, 1),
		accessLogger: logger,
		errorLogger:  logger,
	}

	// Legacy key format tests
	tc.newKeyFormat = false
	tc.prefix = ""
	result, expected = tc.objectKey("1234", cache.CAS), "cas/1234"
	checkTestCase(t, result, expected, "legacy format without prefix")

	tc.prefix = "test"
	result, expected = tc.objectKey("1234", cache.CAS), "test/cas/1234"
	checkTestCase(t, result, expected, "legacy format with prefix")

	// New key format tests
	tc.newKeyFormat = true

	tc.prefix = ""
	result, expected = tc.objectKey("1234", cache.CAS), "cas/12/1234"
	checkTestCase(t, result, expected, "new format with prefix")

	tc.prefix = "test"
	result, expected = tc.objectKey("1234", cache.CAS), "test/cas/12/1234"
	checkTestCase(t, result, expected, "new format with prefix")
}

func checkTestCase(t *testing.T, result string, expected string, testCase string) {
	if result != expected {
		t.Errorf("%s objectKey did not match. (result: '%s' expected: '%s'", testCase, result, expected)
	}
}
