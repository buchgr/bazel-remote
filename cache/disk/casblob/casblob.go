package casblob

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	"github.com/buchgr/bazel-remote/v2/cache/disk/zstdimpl"
)

type CompressionType uint8

const (
	Identity  CompressionType = 0
	Zstandard CompressionType = 1
)

const defaultChunkSize = 1024 * 1024 * 1 // 1M

// 4 bytes, to be written to disk in little-endian format.
// https://github.com/facebook/zstd/blob/dev/doc/zstd_compression_format.md#skippable-frames
const skippableFrameMagicNumber = 0x184D2A50

// Compressed CAS blobs are stored in .zst format, with a header which
// describes the locations of independently compressed chunks.
type header struct {
	// The following data is stored in little-endian format on disk.

	// We start with two zstd skippable frame fields:

	//magicNumber uint32 // 4 bytes: skippableFrameMagicNumber

	// With 1M chunks this gives a limit of ~511TB which is plenty.
	//frameSize   uint32 // 4 bytes: the size of the rest of the header.

	// Then we put our metadata:

	uncompressedSize int64           // 8 bytes
	compression      CompressionType // uint8, 1 byte
	chunkSize        uint32          // 4 bytes

	// Offsets in the file on disk, of each chunk, with an additional
	// entry for the end of the file.
	//
	// Stored as an int64 number of chunks, followed by their int64 offsets,
	// and a final value for the size of the file (header + data).
	// 8 bytes + (n+1)*8 bytes.
	chunkOffsets []int64
}

const chunkTableOffset = 4 + 4 + 8 + 1 + 4 + 8

// Returns the size of the header itself.
func (h *header) size() int64 {
	return chunkTableOffset + (int64(len(h.chunkOffsets)) * 8)
}

func (h *header) frameSize() uint32 {
	return chunkTableOffset + (uint32(len(h.chunkOffsets)) * 8) - 4 - 4
}

// Provides an io.ReadCloser that returns uncompressed data from a cas blob.
type readCloserWrapper struct {
	*header

	rdr io.Reader // Read from this, not from decoder or file.

	rdrCloser io.Closer // Might be nil.

	file *os.File
}

var errWrongMagicNum = errors.New("expected magic number not found")

// Read the header and leave f at the start of the data.
func readHeader(f *os.File) (*header, error) {
	var err error
	var h header

	fileInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	foundFileSize := fileInfo.Size()
	if foundFileSize <= (chunkTableOffset + 16) {
		// The file must have a header with at least two chunkOffsets.
		return nil, fmt.Errorf("file too small (%d) than the minimum header size (%d)",
			foundFileSize, (chunkTableOffset + 16))
	}

	var magicNumber uint32
	err = binary.Read(f, binary.LittleEndian, &magicNumber)
	if err != nil {
		return nil, fmt.Errorf("unable to read magic number: %w", err)
	}
	if magicNumber != skippableFrameMagicNumber {
		return nil, errWrongMagicNum
	}

	var frameSize uint32
	err = binary.Read(f, binary.LittleEndian, &frameSize)
	if err != nil {
		return nil, fmt.Errorf("unable to read frameSize: %w", err)
	}

	err = binary.Read(f, binary.LittleEndian, &h.uncompressedSize)
	if err != nil {
		return nil, err
	}

	err = binary.Read(f, binary.LittleEndian, &h.compression)
	if err != nil {
		return nil, err
	}

	err = binary.Read(f, binary.LittleEndian, &h.chunkSize)
	if err != nil {
		return nil, err
	}

	var numOffsets int64
	err = binary.Read(f, binary.LittleEndian, &numOffsets)
	if err != nil {
		return nil, err
	}

	if numOffsets < 2 {
		// chunkOffsets has an extra entry to specify the compressed file size.
		// Note that we never store empty files.
		return nil, fmt.Errorf("internal error: need at least one chunk, found %d", numOffsets-1)
	}

	metadataSize := numOffsets*8 + 8 + 1 + 4 + 8
	if int64(frameSize) != metadataSize {
		return nil, fmt.Errorf("metadata frame size %d, but metadata size %d",
			frameSize, metadataSize)
	}

	h.chunkOffsets = make([]int64, numOffsets)
	err = binary.Read(f, binary.LittleEndian, h.chunkOffsets)
	if err != nil {
		return nil, err
	}

	prevOffset := int64(-1)
	for i := 0; int64(i) < numOffsets; i++ {
		if h.chunkOffsets[i] <= prevOffset {
			return nil,
				fmt.Errorf("offset table values should increase: %d -> %d",
					h.chunkOffsets[i], prevOffset)
		}
		prevOffset = h.chunkOffsets[i]
	}

	if prevOffset != foundFileSize {
		return nil,
			fmt.Errorf("final offset in chunk table %d should be file size %d",
				prevOffset, foundFileSize)
	}

	return &h, nil
}

// Extract the logical size of a v2 cas blob from rc, and return that
// size along with an equivalent io.ReadCloser to rc.
func ExtractLogicalSize(rc io.ReadCloser) (io.ReadCloser, int64, error) {

	// Read the first part of the header: magic number (4 bytes),
	// frame size (4 bytes), uncompressed size (8 bytes).
	interesting := 16
	earlyHeader := make([]byte, interesting)

	n, err := io.ReadFull(rc, earlyHeader)
	if err != nil {
		return nil, -1, err
	}
	if n != 16 {
		return nil, -1, fmt.Errorf("Tried to read 16 header bytes, only read %d", n)
	}

	var uncompressedSize int64
	br := bytes.NewReader(earlyHeader[8:])
	err = binary.Read(br, binary.LittleEndian, &uncompressedSize)
	if err != nil {
		return nil, -1, err
	}
	if uncompressedSize <= 0 {
		return nil, -1, fmt.Errorf("Expected blob to have positive size, found %d",
			uncompressedSize)
	}

	return &multiReadCloser{
		Reader: io.MultiReader(bytes.NewReader(earlyHeader), rc),
		rc:     rc,
	}, uncompressedSize, nil
}

type multiReadCloser struct {
	io.Reader // This will be a MultiReader.
	rc        io.ReadCloser
}

func (m *multiReadCloser) Close() error {
	return m.rc.Close()
}

// Returns an io.ReadCloser that provides uncompressed data. The caller
// must close the returned io.ReadCloser if it is non-nil. Doing so
// will automatically close f. If there is an error f will be closed, the caller
// does not need to do so.
func GetUncompressedReadCloser(zstd zstdimpl.ZstdImpl, f *os.File, expectedSize int64, offset int64) (io.ReadCloser, error) {
	h, err := readHeader(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	if expectedSize != -1 && h.uncompressedSize != expectedSize {
		f.Close()
		return nil, fmt.Errorf("expected a blob of size %d, found %d",
			expectedSize, h.uncompressedSize)
	}

	if h.compression == Identity {
		// Simple case. Assumes that we only have one chunk if the data is
		// uncompressed (which makes sense).

		if offset > 0 {
			_, err = f.Seek(offset, io.SeekCurrent)
			if err != nil {
				f.Close()
				return nil, err
			}
		}

		return f, nil
	}

	if h.compression != Zstandard {
		f.Close()
		return nil,
			fmt.Errorf("internal error: unsupported compression type %d",
				h.compression)
	}

	// Find the first relevant chunk.
	chunkNum := int64(offset / int64(h.chunkSize))
	remainder := offset % int64(h.chunkSize)

	if chunkNum > 0 {
		_, err = f.Seek(h.chunkOffsets[chunkNum], io.SeekStart)
		if err != nil {
			f.Close()
			return nil, err
		}
	}
	if remainder == 0 {
		dec, err := zstd.GetDecoder(f)
		if err != nil {
			f.Close()
			return nil, err
		}

		return &readCloserWrapper{
			header:    h,
			rdr:       dec,
			rdrCloser: dec,
			file:      f,
		}, nil
	}

	compressedFirstChunk := make([]byte, h.chunkOffsets[chunkNum+1]-h.chunkOffsets[chunkNum])
	_, err = io.ReadFull(f, compressedFirstChunk)
	if err != nil {
		f.Close()
		return nil, err
	}

	uncompressedFirstChunk, err := zstd.DecodeAll(compressedFirstChunk)
	if err != nil {
		f.Close()
		return nil, err
	}

	if chunkNum == int64(len(h.chunkOffsets)-2) {
		// Last chunk in the file.
		r := bytes.NewReader(uncompressedFirstChunk[remainder:])
		f.Close()
		return io.NopCloser(r), nil
	}

	z, err := zstd.GetDecoder(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	br := bytes.NewReader(uncompressedFirstChunk[remainder:])

	return &readCloserWrapper{
		header:    h,
		rdr:       io.MultiReader(br, z),
		rdrCloser: z,
		file:      f,
	}, nil
}

// Returns an io.ReadCloser that provides zstandard compressed data. The
// caller must close the returned io.ReadCloser if it is non-nil. Doing so
// will automatically close f. If there is an error f will be closed, the caller
// does not need to do so.
func GetZstdReadCloser(zstd zstdimpl.ZstdImpl, f *os.File, expectedSize int64, offset int64) (io.ReadCloser, error) {

	h, err := readHeader(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	if expectedSize != -1 && h.uncompressedSize != expectedSize {
		f.Close()
		return nil, fmt.Errorf("expected a blob of size %d, found %d",
			expectedSize, h.uncompressedSize)
	}

	if h.compression == Identity {
		// Simple case. Assumes that we only have one chunk if the data is
		// uncompressed (which makes sense).

		if offset > 0 {
			_, err = f.Seek(offset, io.SeekCurrent)
			if err != nil {
				f.Close()
				return nil, err
			}
		}

		return GetLegacyZstdReadCloser(zstd, f)
	}

	if h.compression != Zstandard {
		f.Close()
		return nil, fmt.Errorf("unsupported compression type: %d",
			h.compression)
	}

	if offset == 0 {
		// Special case for full reads: stream the header also.
		//
		// When using compressed storage mode and the grpc proxy backend, the
		// frontend bazel-remote expects to receive the casblob header.

		_, err = f.Seek(0, io.SeekStart)
		if err != nil {
			return nil, fmt.Errorf("failed to seek to start of file: %w", err)
		}

		return f, nil
	}

	// Find the first relevant chunk.
	chunkNum := int64(offset / int64(h.chunkSize))
	remainder := offset % int64(h.chunkSize)

	if chunkNum > 0 {
		_, err = f.Seek(h.chunkOffsets[chunkNum], io.SeekStart)
		if err != nil {
			f.Close()
			return nil, err
		}
	}

	if remainder == 0 {
		// Simple case- just stream the file from here.
		return f, nil
	}

	compressedFirstChunk := make([]byte, h.chunkOffsets[chunkNum+1]-h.chunkOffsets[chunkNum])
	_, err = io.ReadFull(f, compressedFirstChunk)
	if err != nil {
		f.Close()
		return nil, err
	}

	uncompressedFirstChunk, err := zstd.DecodeAll(compressedFirstChunk)
	if err != nil {
		f.Close()
		return nil, err
	}

	chunkToRecompress := uncompressedFirstChunk[remainder:]
	recompressedChunk := zstd.EncodeAll(chunkToRecompress)

	br := bytes.NewReader(recompressedChunk)
	if chunkNum == int64(len(h.chunkOffsets)-2) {
		f.Close()
		return io.NopCloser(br), nil
	}

	return &readCloserWrapper{
		header: h,
		rdr:    io.MultiReader(br, f),
		file:   f,
	}, nil
}

// GetLegacyZstdReadCloser returns an io.ReadCloser that provides
// zstandard-compressed data from an uncompressed file.
func GetLegacyZstdReadCloser(zstd zstdimpl.ZstdImpl, f *os.File) (io.ReadCloser, error) {

	pr, pw := io.Pipe()

	enc, err := zstd.GetEncoder(pw)
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	go func() {
		// Read from the file, write to enc.

		// TODO: consider implementing something with a timeout?
		_, err := enc.ReadFrom(f)
		if err != nil {
			log.Println("Error while reading/compressing file:", err)

			// Reading from pr will now receive this error:
			_ = pw.CloseWithError(err)
		}

		err = enc.Close()
		if err != nil {
			log.Println("Error while closing encoder:", err)
			_ = pw.CloseWithError(err)
		}

		err = f.Close()
		if err != nil {
			log.Println("Error while closing file:", err)
			_ = pw.CloseWithError(err)
		}

		_ = pw.Close()
	}()

	return pr, nil
}

func (h *header) write(f *os.File) error {
	var err error

	err = binary.Write(f, binary.LittleEndian, uint32(skippableFrameMagicNumber))
	if err != nil {
		return err
	}

	err = binary.Write(f, binary.LittleEndian, h.frameSize())
	if err != nil {
		return err
	}

	err = binary.Write(f, binary.LittleEndian, h.uncompressedSize)
	if err != nil {
		return err
	}

	err = binary.Write(f, binary.LittleEndian, h.compression)
	if err != nil {
		return err
	}

	err = binary.Write(f, binary.LittleEndian, h.chunkSize)
	if err != nil {
		return err
	}

	err = binary.Write(f, binary.LittleEndian, int64(len(h.chunkOffsets)))
	if err != nil {
		return err
	}

	return binary.Write(f, binary.LittleEndian, h.chunkOffsets)
}

func (b *readCloserWrapper) Read(p []byte) (int, error) {
	return b.rdr.Read(p)
}

func (b *readCloserWrapper) Close() error {
	if b.rdrCloser != nil {
		_ = b.rdrCloser.Close()
		b.rdrCloser = nil
	}

	if b.file == nil {
		return nil
	}

	f := b.file
	b.file = nil

	return f.Close()
}

// sync pool to reuse large byte buffer between multiple go routines
var chunkBufferPool = &sync.Pool{
	New: func() any {
		b := make([]byte, defaultChunkSize)
		return &b
	},
}

// Read from r and write to f, using CompressionType t.
// Return the size on disk or an error if something went wrong.
func WriteAndClose(zstd zstdimpl.ZstdImpl, r io.Reader, f *os.File, t CompressionType, hash string, size int64) (int64, error) {
	var err error
	defer f.Close()

	if size <= 0 {
		return -1, fmt.Errorf("invalid file size: %d", size)
	}

	chunkSize := uint32(defaultChunkSize)

	numChunks := int64(1)
	remainder := int64(0)
	if t == Zstandard {
		numChunks = size / int64(chunkSize)
		remainder = size % int64(chunkSize)
		if remainder > 0 {
			numChunks++
		}
	}

	numOffsets := numChunks + 1
	h := header{
		uncompressedSize: size,
		compression:      t,
		chunkSize:        chunkSize,
		chunkOffsets:     make([]int64, numOffsets),
	}

	h.chunkOffsets[0] = chunkTableOffset

	err = h.write(f)
	if err != nil {
		return -1, err
	}

	fileOffset := h.size()

	var n int64

	if t == Identity {
		hasher := sha256.New()

		n, err = io.Copy(io.MultiWriter(f, hasher), r)
		if err != nil {
			return -1, err
		}
		if n != size {
			return -1, fmt.Errorf("expected to copy %d bytes, actually copied %d bytes",
				size, n)
		}

		actualHash := hex.EncodeToString(hasher.Sum(nil))
		if actualHash != hash {
			return -1,
				fmt.Errorf("checksums don't match. Expected %s, found %s",
					hash, actualHash)
		}

		return n + fileOffset, f.Close()
	}

	// Compress the data in chunks...

	nextChunk := 0 // Index in h.chunkOffsets.
	remainingRawData := size
	var numRead int

	chunkBufferPtr := chunkBufferPool.Get().(*[]byte)
	defer func() {
		chunkBufferPool.Put(chunkBufferPtr)
	}()
	uncompressedChunk := *chunkBufferPtr

	hasher := sha256.New()

	for nextChunk < len(h.chunkOffsets)-1 {
		h.chunkOffsets[nextChunk] = fileOffset
		nextChunk++

		chunkEnd := int64(chunkSize)
		if remainingRawData <= int64(chunkSize) {
			chunkEnd = remainingRawData
		}
		remainingRawData -= chunkEnd

		numRead, err = io.ReadFull(r, uncompressedChunk[0:chunkEnd])
		if err != nil {
			return -1, fmt.Errorf("Only managed to read %d of %d bytes: %w", numRead, chunkEnd, err)
		}

		compressedChunk := zstd.EncodeAll(uncompressedChunk[0:chunkEnd])

		hasher.Write(uncompressedChunk[0:chunkEnd])

		written, err := f.Write(compressedChunk)
		if err != nil {
			return -1, fmt.Errorf("Failed to write compressed chunk to disk: %w", err)
		}

		fileOffset += int64(written)
	}
	h.chunkOffsets[nextChunk] = fileOffset

	// Confirm that there is no data left to be read.
	bytesAfter, err := io.ReadFull(r, uncompressedChunk)
	if err == nil {
		return -1, fmt.Errorf("expected %d bytes but got at least %d more", size, bytesAfter)
	} else if err != io.EOF {
		return -1, fmt.Errorf("Failed to read chunk of size %d: %w", len(uncompressedChunk), err)
	}

	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != hash {
		return -1, fmt.Errorf("checksums don't match. Expected %s, found %s",
			hash, actualHash)
	}

	// We know all the chunk offsets now, go back and fill those in.
	_, err = f.Seek(chunkTableOffset, io.SeekStart)
	if err != nil {
		return -1, fmt.Errorf("Failed to seek to offset %d: %w", chunkTableOffset, err)
	}

	err = binary.Write(f, binary.LittleEndian, h.chunkOffsets)
	if err != nil {
		return -1, fmt.Errorf("Failed to write chunk offsets: %w", err)
	}

	err = f.Sync()
	if err != nil {
		return -1, fmt.Errorf("Failed to sync file: %w", err)
	}

	err = f.Close()
	if err != nil {
		return -1, fmt.Errorf("Failed to close file: %w", err)
	}

	return fileOffset, nil
}
