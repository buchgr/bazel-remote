package casblob

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"

	"github.com/buchgr/bazel-remote/utils/zstdpool"
	"github.com/klauspost/compress/zstd"
	syncpool "github.com/mostynb/zstdpool-syncpool"
)

type CompressionType uint8

const (
	Identity  CompressionType = 0
	Zstandard CompressionType = 1
)

var zstdFastestLevel = zstd.WithEncoderLevel(zstd.SpeedFastest)

var encoder, _ = zstd.NewWriter(nil, zstdFastestLevel) // TODO: raise WithEncoderConcurrency ?
var decoder, _ = zstd.NewReader(nil)                   // TODO: raise WithDecoderConcurrency ?

var encoderPool = zstdpool.GetEncoderPool()
var decoderPool = zstdpool.GetDecoderPool()

var errDecoderPoolFail error = errors.New("failed to get DecoderWrapper from pool")
var errEncoderPoolFail error = errors.New("failed to get EncoderWrapper from pool")

const defaultChunkSize = 1024 * 1024 * 1 // 1M

// CAS blobs are stored with a header followed by chunks of data in either
// compressed or uncompressed format.
type header struct {
	// The following data is stored in little-endian format on disk.
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

const chunkTableOffset = 8 + 1 + 4 + 8

// Returns the size of the header itself.
func (h *header) size() int64 {
	return chunkTableOffset + (int64(len(h.chunkOffsets)) * 8)
}

// Provides an io.ReadCloser that returns uncompressed data from a cas blob.
type readCloserWrapper struct {
	*header

	rdr io.Reader // Read from this, not from decoder or file.

	decoder *syncpool.DecoderWrapper // Might be nil.
	file    *os.File
}

// Read the header and leave f at the start of the data.
func readHeader(f *os.File) (*header, error) {
	var err error
	var h header

	fileInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	foundFileSize := fileInfo.Size()

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
		return nil, fmt.Errorf("internal error: need at least one chunk, found %d", numOffsets-1)
	}

	prevOffset := int64(-1)
	h.chunkOffsets = make([]int64, numOffsets, numOffsets)
	for i := 0; int64(i) < numOffsets; i++ {
		err = binary.Read(f, binary.LittleEndian, &h.chunkOffsets[i])
		if err != nil {
			return nil, err
		}

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

func GetLogicalSize(filename string) (int64, error) {
	f, err := os.Open(filename)
	if err != nil {
		return -1, err
	}
	defer f.Close()

	hdr, err := readHeader(f)
	if err != nil {
		return -1, fmt.Errorf("Failed to read %s: %w", filename, err)
	}

	return hdr.uncompressedSize, nil
}

// Closes f if there is an error. Otherwise the caller must Close the returned
// io.ReadCloser.
func GetUncompressedReadCloser(f *os.File, expectedSize int64, offset int64) (io.ReadCloser, error) {
	h, err := readHeader(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	if expectedSize != -1 && h.uncompressedSize != expectedSize {
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
		f.Seek(h.chunkOffsets[chunkNum], io.SeekStart)
	}
	if remainder == 0 {
		z, ok := decoderPool.Get().(*syncpool.DecoderWrapper)
		if !ok {
			f.Close()
			return nil, err
		}
		err := z.Reset(f)
		if err != nil {
			f.Close()
			return nil, err
		}

		return &readCloserWrapper{
			header:  h,
			rdr:     z,
			decoder: z,
			file:    f,
		}, nil
	}

	compressedFirstChunk := make([]byte, h.chunkOffsets[chunkNum+1]-h.chunkOffsets[chunkNum])
	_, err = io.ReadFull(f, compressedFirstChunk)
	if err != nil {
		f.Close()
		return nil, err
	}

	uncompressedFirstChunk, err := decoder.DecodeAll(compressedFirstChunk, nil)
	if err != nil {
		f.Close()
		return nil, err
	}

	if chunkNum == int64(len(h.chunkOffsets)-2) {
		// Last chunk in the file.
		r := bytes.NewReader(uncompressedFirstChunk[remainder:])
		f.Close()
		return ioutil.NopCloser(r), nil
	}

	z, ok := decoderPool.Get().(*syncpool.DecoderWrapper)
	if !ok {
		f.Close()
		return nil, errDecoderPoolFail
	}

	err = z.Reset(f)
	if err != nil {
		f.Close()
		decoderPool.Put(z)
		return nil, fmt.Errorf("Failed to setup zstd decoder: %w", err)
	}

	br := bytes.NewReader(uncompressedFirstChunk[remainder:])

	return &readCloserWrapper{
		header:  h,
		rdr:     io.MultiReader(br, z),
		decoder: z,
		file:    f,
	}, nil
}

// Closes f if there is an error. Otherwise the caller must Close the returned
// io.ReadCloser.
func GetZstdReadCloser(f *os.File, expectedSize int64, offset int64) (io.ReadCloser, error) {

	h, err := readHeader(f)
	if err != nil {
		f.Close()
		return nil, err
	}

	if expectedSize != -1 && h.uncompressedSize != expectedSize {
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

		pr, pw := io.Pipe()

		enc, ok := encoderPool.Get().(*syncpool.EncoderWrapper)
		if !ok {
			err = errEncoderPoolFail
		}
		if err != nil {
			f.Close()
			return nil, err
		}
		enc.Reset(pw)

		go func() {
			// Read from the file, write to enc.

			// TODO: consider implementing something with a timeout?
			_, err := enc.ReadFrom(f)
			if err != nil {
				// We can't do anything here except log an error.
				log.Println("Error while compressing file:", err)
			}

			enc.Close()
			f.Close()
		}()

		return pr, nil
	}

	if h.compression != Zstandard {
		f.Close()
		return nil, fmt.Errorf("unsupported compression type: %d",
			h.compression)
	}

	// Find the first relevant chunk.
	chunkNum := int64(offset / int64(h.chunkSize))
	remainder := offset % int64(h.chunkSize)

	if chunkNum > 0 {
		f.Seek(h.chunkOffsets[chunkNum], io.SeekStart)
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

	uncompressedFirstChunk, err := decoder.DecodeAll(compressedFirstChunk, nil)
	if err != nil {
		f.Close()
		return nil, err
	}

	chunkToRecompress := uncompressedFirstChunk[remainder:]
	recompressedChunk := encoder.EncodeAll(chunkToRecompress, nil)

	br := bytes.NewReader(recompressedChunk)
	if chunkNum == int64(len(h.chunkOffsets)-2) {
		f.Close()
		return ioutil.NopCloser(br), nil
	}

	return &readCloserWrapper{
		header: h,
		rdr:    io.MultiReader(br, f),
		file:   f,
	}, nil
}

func (h *header) write(f *os.File) error {
	var err error

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

	return h.writeChunkTable(f)
}

func (h *header) writeChunkTable(f *os.File) error {
	var err error

	for _, o := range h.chunkOffsets {
		err = binary.Write(f, binary.LittleEndian, o)
		if err != nil {
			return err
		}
	}

	return nil
}

func (b *readCloserWrapper) Read(p []byte) (int, error) {
	return b.rdr.Read(p)
}

func (b *readCloserWrapper) Close() error {
	if b.decoder != nil {
		b.decoder.Close()
		b.decoder = nil
	}

	if b.file == nil {
		return nil
	}

	f := b.file
	b.file = nil

	return f.Close()
}

// Read from r and write to f, using CompressionType t.
// Return the size on disk or an error if something went wrong.
func WriteAndClose(r io.Reader, f *os.File, t CompressionType, hash string, size int64) (int64, error) {
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
		chunkOffsets:     make([]int64, numOffsets, numOffsets),
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

	uncompressedChunk := make([]byte, chunkSize, chunkSize)

	hasher := sha256.New()

	for nextChunk < len(h.chunkOffsets)-1 {
		h.chunkOffsets[nextChunk] = fileOffset
		nextChunk++

		chunkEnd := int64(chunkSize)
		if remainingRawData <= int64(chunkSize) {
			chunkEnd = remainingRawData
		}
		remainingRawData -= chunkEnd

		_, err = io.ReadFull(r, uncompressedChunk[0:chunkEnd])
		if err != nil {
			return -1, err
		}

		compressedChunk := encoder.EncodeAll(uncompressedChunk[0:chunkEnd], nil)

		hasher.Write(uncompressedChunk[0:chunkEnd])

		written, err := f.Write(compressedChunk)
		if err != nil {
			return -1, err
		}

		fileOffset += int64(written)
	}
	h.chunkOffsets[nextChunk] = fileOffset

	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != hash {
		return -1, fmt.Errorf("checksums don't match. Expected %s, found %s",
			hash, actualHash)
	}

	// We know all the chunk offsets now, go back and fill those in.
	_, err = f.Seek(chunkTableOffset, io.SeekStart)
	if err != nil {
		return -1, err
	}

	err = h.writeChunkTable(f)
	if err != nil {
		return -1, err
	}

	err = f.Sync()
	if err != nil {
		return -1, err
	}

	return fileOffset, f.Close()
}
