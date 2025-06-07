package sha256verifier

import (
	"bytes"
	"testing"
)

type bufferWithFakeCloser struct {
	bytes.Buffer
}

func (b *bufferWithFakeCloser) Close() error {
	return nil
}

func TestHashVerification(t *testing.T) {

	var hashVerificationTests = []struct {
		expectedSize int64
		expectedHash string
		data         []byte
		success      bool
	}{
		{int64(0), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", []byte{}, true},
		{int64(0), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", []byte{0}, false},
		{int64(0), "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85", []byte{}, false},
		{int64(4), "9f64a747e1b97f131fabb6b447296c9b6f0201e79fb3c5356e6c77e89b6a806a", []byte{1, 2, 3, 4}, true},
		{int64(3), "9f64a747e1b97f131fabb6b447296c9b6f0201e79fb3c5356e6c77e89b6a806a", []byte{1, 2, 3, 4}, false},
		{int64(4), "9f64a747e1b97f131fabb6b447296c9b6f0201e79fb3c5356e6c77e89b6a806a", []byte{1, 2, 3, 4, 0}, false},
	}

	for _, tt := range hashVerificationTests {

		var buf bufferWithFakeCloser

		h := New(tt.expectedHash, tt.expectedSize, &buf)
		if h == nil {
			t.Fatal("Expected non-nil sha256verifier")
		}

		n, err := h.Write(tt.data)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(tt.data) {
			t.Fatalf("Error: incomplete Write: %d, expected: %d", n, len(tt.data))
		}

		err = h.Close()
		if tt.success {
			if err != nil {
				t.Fatal("Error: ", err, "expected hash", tt.expectedSize, tt.expectedHash)
			}

			if !bytes.Equal(tt.data, buf.Bytes()) {
				t.Fatal("Error: mismatched data written")
			}

		} else {
			if err == nil {
				t.Fatal("Expected failure for", tt.data)
			}
		}
	}
}
