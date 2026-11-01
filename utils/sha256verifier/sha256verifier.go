package sha256verifier

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
)

type sha256verifier struct {
	hash.Hash
	expectedSize        int64
	expectedHash        string
	actualSize          int64
	multiWriter         io.Writer
	originalWriteCloser io.WriteCloser
}

func New(expectedHash string, expectedSize int64, writeCloser io.WriteCloser) *sha256verifier {
	hash := sha256.New()

	return &sha256verifier{
		Hash:                hash,
		expectedHash:        expectedHash,
		expectedSize:        expectedSize,
		multiWriter:         io.MultiWriter(hash, writeCloser),
		originalWriteCloser: writeCloser,
	}
}

func (s *sha256verifier) Write(p []byte) (int, error) {

	n, err := s.multiWriter.Write(p)
	if n > 0 {
		s.actualSize += int64(n)
	}

	return n, err
}

// ErrMismatch is wrapped by the errors of Close about a size or hash mismatch.
var ErrMismatch = errors.New("mismatch")

// Close closes the underlying writer, and reports a size or hash mismatch, or else the error
// from closing.
func (s *sha256verifier) Close() error {
	closeErr := s.originalWriteCloser.Close()

	if s.actualSize != s.expectedSize {
		return fmt.Errorf("size %w: expected %d bytes, got %d", ErrMismatch, s.expectedSize, s.actualSize)
	}

	actualHash := hex.EncodeToString(s.Sum(nil))
	if actualHash != s.expectedHash {
		return fmt.Errorf("hash %w: expected %s, got %s", ErrMismatch, s.expectedHash, actualHash)
	}

	return closeErr
}
