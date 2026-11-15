package grpcproxy

import (
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type StreamReadCloser[M DataMessage] struct {
	Stream       RecvStream[M]
	buf          []byte
	receivedData bool
}

type DataMessage interface {
	GetData() []byte
}

type RecvStream[M DataMessage] interface {
	Recv() (M, error)
	CloseSend() error
}

func (s *StreamReadCloser[M]) readFromBuf(p []byte) int {
	n := len(s.buf)
	if len(p) < n {
		n = len(p)
	}
	copy(p, s.buf[:n])
	if n == len(s.buf) {
		s.buf = nil
	} else {
		s.buf = s.buf[n:]
	}
	return n
}

func (s *StreamReadCloser[M]) Read(p []byte) (int, error) {
	n := 0
	if s.buf != nil {
		n = s.readFromBuf(p)
	}
	if n == len(p) {
		return n, nil
	}
	msg, err := s.Stream.Recv()
	if err != nil {
		if err == io.EOF {
			if closeErr := s.Stream.CloseSend(); closeErr != nil {
				return n, closeErr
			}
		} else if s.receivedData && status.Code(err) == codes.NotFound {
			// An absence after payload is an incomplete transfer, not a cache miss.
			err = status.Errorf(codes.Internal, "upstream read failed after payload: %v", err)
		}
		return n, err
	}

	s.buf = msg.GetData()
	s.receivedData = s.receivedData || len(s.buf) > 0
	n += s.readFromBuf(p[n:])
	return n, err
}

func (s *StreamReadCloser[M]) Close() error {
	return s.Stream.CloseSend()
}
