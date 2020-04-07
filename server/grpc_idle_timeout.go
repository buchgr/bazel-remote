package server

import (
	"context"

	"google.golang.org/grpc"

	"github.com/buchgr/bazel-remote/utils/idle"
)

type GrpcIdleTimer struct {
	idleTimer *idle.Timer
}

func NewGrpcIdleTimer(idleTimer *idle.Timer) *GrpcIdleTimer {
	return &GrpcIdleTimer{idleTimer: idleTimer}
}

func (t *GrpcIdleTimer) StreamServerInterceptor(srv interface{},
	ss grpc.ServerStream, info *grpc.StreamServerInfo,
	handler grpc.StreamHandler) error {

	t.idleTimer.ResetTimer()
	return handler(srv, ss)
}

func (t *GrpcIdleTimer) UnaryServerInterceptor(ctx context.Context,
	req interface{}, info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler) (interface{}, error) {

	t.idleTimer.ResetTimer()
	return handler(ctx, req)
}
