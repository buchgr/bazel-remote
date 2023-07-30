package grpcproxy

import "io"

type StreamReadCloser[M DataMessage] struct {
	Stream RecvStream[M]
	buf    []byte
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
	if err == io.EOF {
		s.Stream.CloseSend()
	} else if err != nil {
		return -1, err
	}
	s.buf = msg.GetData()
	n += s.readFromBuf(p[n:])
	return n, err
}

func (s *StreamReadCloser[M]) Close() error {
	return s.Stream.CloseSend()
}
