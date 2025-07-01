package sha256verifier

import (
	"crypto/sha256"
	"encoding/hex"
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

func (s *sha256verifier) Close() error {
	if s.actualSize != s.expectedSize {
		return fmt.Errorf("Error: expected %d bytes, got %d", s.expectedSize, s.actualSize)
	}

	actualHash := hex.EncodeToString(s.Sum(nil))
	if actualHash != s.expectedHash {
		return fmt.Errorf("Error: expected hash %s, got %s", s.expectedHash, actualHash)
	}

	err := s.originalWriteCloser.Close()
	if err != nil {
		return err
	}

	return nil
}
