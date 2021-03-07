package server

import (
  "context"
  "fmt"
  "net/http"
  "io/ioutil"

  metrics "github.com/buchgr/bazel-remote/genproto/metrics"
  "github.com/buchgr/bazel-remote/cache"
)

type MetricsServiceServer struct {
	Port string
  AccessLogger cache.Logger
  ErrorLogger cache.Logger
}

func (s *MetricsServiceServer) GetMetrics(ctx context.Context, request *metrics.Empty) (*metrics.MetricsResponse, error) {
  resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%s/metrics", s.Port))
  if err != nil {
    s.ErrorLogger.Printf("GRPC GETMETRICS ERROR: %s", err.Error())
    return nil, err
  }

  defer resp.Body.Close()

  body, err := ioutil.ReadAll(resp.Body)
  if err != nil {
    s.ErrorLogger.Printf("GRPC GETMETRICS ERROR: %s", err.Error())
    return nil, err
  }
  s.AccessLogger.Printf("GRPC GETMETRICS OK")
	return &metrics.MetricsResponse{Data: string(body)}, nil
}
