package server

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"strconv"
	"strings"

	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/buchgr/bazel-remote/cache"
)

const (
	// The maximum chunk size to write back to the client in Send calls.
	// Inspired by Goma's FileBlob.FILE_CHUNK maxium size.
	maxChunkSize = 2 * 1024 * 1024 // 2M
)

var (
	// Used by seekWriterTo to simulate seeking in Writers that aren't
	// Seekers, without reallocating each time.
	zeros = [1024 * 1024]byte{} // 1M
)

// ByteStreamServer interface:

func (s *grpcServer) Read(req *bytestream.ReadRequest,
	resp bytestream.ByteStream_ReadServer) error {

	if req.ReadOffset < 0 {
		s.accessLogger.Printf("GRPC BYTESTREAM READ OFFSET OUT OF RANGE: %s %d",
			req.ReadOffset)
		return status.Error(codes.OutOfRange,
			"Negative ReadOffset is out of range")
	}

	if req.ReadLimit < 0 {
		s.accessLogger.Printf("GRPC BYTESTREAM READ LIMIT OUT OF RANGE: %s %d",
			req.ReadLimit)
		return status.Error(codes.OutOfRange,
			"Negative ReadLimit is out of range")
	}

	limitedSend := req.ReadLimit != 0

	// req.ResourceName should be of the format:
	// [{instance_name}]/blobs/{hash}/{size}

	errorPrefix := "GRPC BYTESTREAM READ"

	fields := strings.Split(req.ResourceName, "/")
	instanceName := ""
	var rem []string
	for i := range fields {
		if fields[i] == "blobs" {
			if i > 0 {
				instanceName = fields[i-1]
			}
			rem = fields[i+1:]
			break
		}
	}

	if len(rem) != 2 {
		msg := fmt.Sprintf("Unable to parse resource name: %s", req.ResourceName)
		s.accessLogger.Printf("%s: %s", errorPrefix, msg)
		return status.Error(codes.InvalidArgument, msg)
	}

	hash := rem[0]
	err := s.validateHash(hash, errorPrefix)
	if err != nil {
		return err
	}

	size, err := strconv.ParseInt(rem[1], 10, 64)
	if err != nil {
		s.accessLogger.Printf("GRPC BYTESTREAM READ FAILED: unable to parse size: %s", err)
		return status.Error(codes.Unknown, err.Error())
	}

	if req.ReadOffset > size {
		msg := fmt.Sprintf("ReadOffset %d larger than expected data size %d resource: %s",
			req.ReadOffset, size, req.ResourceName)
		s.accessLogger.Printf("GRPC BYTESTREAM READ FAILED %s: %s", hash, msg)
		return status.Error(codes.OutOfRange, msg)
	}

	rdr, sizeBytes, err := s.cache.Get(cache.CAS, instanceName, hash)
	if err != nil {
		msg := fmt.Sprintf("GRPC BYTESTREAM READ FAILED: %v", err)
		s.accessLogger.Printf(msg)
		return status.Error(codes.Unknown, msg)
	}
	if rdr == nil {
		msg := fmt.Sprintf("GRPC BYTESTREAM READ BLOB NOT FOUND: %s", hash)
		s.accessLogger.Printf(msg)
		return status.Error(codes.NotFound, msg)
	}

	if sizeBytes != size {
		msg := fmt.Sprintf("Retrieved item had size %d expected %d",
			sizeBytes, size)
		s.accessLogger.Printf("GRPC BYTESTREAM READ FAILED (BAD ITEM?): %v", err)
		return status.Error(codes.DataLoss, msg)
	}

	bufSize := size
	if bufSize > maxChunkSize {
		bufSize = maxChunkSize
	}

	buf := make([]byte, bufSize)

	if req.ReadOffset > 0 {
		seekReaderTo(rdr, req.ReadOffset)
	}

	sendLimitRemaining := req.ReadLimit

	var chunkResp bytestream.ReadResponse
	for {
		n, err := rdr.Read(buf)
		if err == io.EOF {
			s.accessLogger.Printf("GRPC BYTESTREAM READ COMPLETED %s",
				req.ResourceName)
			return nil
		}

		if err != nil {
			msg := fmt.Sprintf("GRPC BYTESTREAM READ FAILED: %v", err)
			s.accessLogger.Printf(msg)
			return status.Error(codes.Unknown, msg)
		}

		if limitedSend {
			if (sendLimitRemaining - int64(n)) < 0 {
				msg := "GRPC BYTESTREAM READ FAILED: READ LIMIT EXCEEDED"
				return status.Error(codes.OutOfRange, msg)
			}
			sendLimitRemaining -= int64(n)
		}

		chunkResp.Data = buf[:n]
		err = resp.Send(&chunkResp)
		if err != nil {
			s.accessLogger.Printf("GRPC BYTESTREAM READ FAILED TO SEND RESPONSE: %s", err)
			return status.Error(codes.Unknown, err.Error())
		}
	}
}

// Seek to offset in Reader r, from the current position.
func seekReaderTo(r io.Reader, offset int64) {
	switch s := r.(type) {
	case io.Seeker:
		s.Seek(offset, io.SeekCurrent)
	default:
		io.CopyN(ioutil.Discard, r, offset)
	}
}

// Seek to offset in Writer w, from the current position.
func seekWriterTo(w io.Writer, offset int64) {
	switch s := w.(type) {
	case io.Seeker:
		s.Seek(offset, io.SeekCurrent)
		return
	}

	blockSize := int64(len(zeros))
	if blockSize > offset {
		blockSize = offset
	}

	processed := int64(0)
	for processed < offset {
		if processed+blockSize > offset {
			blockSize = offset - processed
		}
		w.Write(zeros[:blockSize])
		processed += blockSize
	}
	// check: processed == offset
}

// Parse a WriteRequest.ResourceName, return the hash, size and an error.
func (s *grpcServer) parseWriteResource(r string) (string, string, int64, error) {
	// req.ResourceName is of the form:
	// [{instance_name}/]uploads/{uuid}/blobs/{hash}/{size}[/{optionalmetadata}]

	fields := strings.Split(r, "/")
	instanceName := ""
	var rem []string
	for i := range fields {
		if fields[i] == "uploads" {
			if i > 0 {
				instanceName = fields[i-1]
			}
			rem = fields[i+1:]
			break
		}
	}

	if len(rem) < 4 || rem[1] != "blobs" {
		return "", "", 0, status.Errorf(codes.InvalidArgument, "Unable to parse resource name: %s", r)
	}

	hash := rem[2]
	err := s.validateHash(hash, "GRPC BYTESTREAM READ FAILED")
	if err != nil {
		return "", "", 0, err
	}

	size, err := strconv.ParseInt(rem[3], 10, 64)
	if err != nil {
		return "", "", 0, status.Errorf(codes.InvalidArgument, "Unable to parse size: %s", rem[3])
	}

	return instanceName, hash, size, nil
}

func (s *grpcServer) Write(srv bytestream.ByteStream_WriteServer) error {

	var resp bytestream.WriteResponse
	pr, pw := io.Pipe()

	putResult := make(chan error)
	recvResult := make(chan error)
	resourceNameChan := make(chan string, 1)

	go func() {
		firstIteration := true
		var resourceName string
		var size int64

		for {
			req, err := srv.Recv()
			if err == io.EOF {
				if resp.CommittedSize != size {
					err := fmt.Errorf("Unexpected amount of data read: %d expected: %d",
						resp.CommittedSize, size)
					recvResult <- status.Error(codes.Unknown, err.Error())
					return
				}

				recvResult <- io.EOF
				return
			}
			if err != nil {
				recvResult <- status.Error(codes.Internal, err.Error())
				return
			}

			if firstIteration {
				resourceName = req.ResourceName
				if resourceName == "" {
					msg := "Empty resource name"
					s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", msg)
					recvResult <- status.Error(codes.InvalidArgument, msg)
					return
				}
				resourceNameChan <- resourceName
				close(resourceNameChan)

				var instanceName, hash string
				instanceName, hash, size, err = s.parseWriteResource(resourceName)
				if err != nil {
					s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", err)
					recvResult <- err
					return
				}

				resp.CommittedSize = req.WriteOffset
				if req.WriteOffset > 0 {
					// Maybe we should just reject this as an invalid request?
					// We always return 0 in QueryWriteStatus.
					seekWriterTo(pw, req.WriteOffset)
				}

				go func() {
					putResult <- s.cache.Put(cache.CAS, instanceName, hash, size, pr)
				}()

				firstIteration = false
			} else {
				if req.ResourceName != "" && resourceName != req.ResourceName {
					msg := fmt.Sprintf("Resource name changed in a single Write %v -> %v",
						resourceName, req.ResourceName)
					recvResult <- status.Error(codes.InvalidArgument, msg)
					return
				}
			}

			n, err := pw.Write(req.Data)
			if err != nil {
				recvResult <- status.Error(codes.Internal, err.Error())
				return
			}
			resp.CommittedSize += int64(n)

			if resp.CommittedSize > size {
				recvResult <- fmt.Errorf("Client sent more than %d data! %d",
					resp.CommittedSize, size)
				return
			}

			// Possibly redundant check, since we explicitly check for
			// EOF at the start of each loop.
			if req.FinishWrite {
				if resp.CommittedSize != size {
					err := fmt.Errorf("Unexpected amount of data read: %d expected: %d",
						resp.CommittedSize, size)
					recvResult <- status.Error(codes.Unknown, err.Error())
					return
				}

				recvResult <- io.EOF
				return
			}
		}
	}()

	select {
	case err, ok := <-recvResult:
		if !ok {
			msg := "Receive loop closed unexpectedly."
			s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", msg)
			return status.Error(codes.Internal, msg)
		}
		if err == io.EOF {
			pw.Close()
			break
		}
		if err != nil {
			s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s",
				err.Error())
			return err
		}

	case err, ok := <-putResult:
		if !ok {
			msg := "Cache Put closed unexpectedly."
			s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", msg)
			return status.Error(codes.Internal, msg)
		}
		if err == nil {
			// Unexpected early return. Should not happen.
			s.accessLogger.Printf("GRPC BYTESTREAM WRITE CACHE INTERNAL ERROR")
			return status.Error(codes.Internal, "Cache attempt failed.")
		}

		s.accessLogger.Printf("GRPC BYTESTREAM WRITE CACHE ERROR: %s",
			err.Error())
		return err
	}

	err, ok := <-putResult
	if !ok {
		msg := "Cache Put closed unexpectedly."
		s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", msg)
		return status.Error(codes.Unknown, msg)
	}
	if err != nil {
		s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", err)
		return status.Error(codes.Unknown, err.Error())
	}

	err = srv.SendAndClose(&resp)
	if err != nil {
		s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", err)
		return status.Error(codes.Unknown, err.Error())
	}

	s.accessLogger.Printf("GRPC BYTESTREAM WRITE COMPLETED: %s", <-resourceNameChan)
	return nil
}

func (s *grpcServer) QueryWriteStatus(context.Context, *bytestream.QueryWriteStatusRequest) (*bytestream.QueryWriteStatusResponse, error) {
	// This should be equivalent to returning an UNIMPLEMENTED error.
	resp := bytestream.QueryWriteStatusResponse{
		CommittedSize: 0,
		Complete:      false,
	}
	return &resp, nil
}
