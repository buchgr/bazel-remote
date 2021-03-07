package server

import (
  "context"
  "fmt"
  "log"
  // "net"
  "net/http"
  "io/ioutil"
  //
  // "google.golang.org/grpc"
  //
  // "github.com/grpc-ecosystem/go-grpc-prometheus"
  // pb "github.com/grpc-ecosystem/go-grpc-prometheus/examples/grpc-server-with-prometheus/protobuf"
  // "github.com/prometheus/client_golang/prometheus"
  // "github.com/prometheus/client_golang/prometheus/promhttp"
  // "google.golang.org/grpc/reflection"
  metrics "github.com/buchgr/bazel-remote/genproto/metrics"
)

type MetricsServiceServer struct {
	addr string
}

func (s *MetricsServiceServer) GetMetrics(ctx context.Context, request *metrics.Empty) (*metrics.MetricsResponse, error) {
  resp, err := http.Get(fmt.Sprintf("http://%s", s.addr))
  if err != nil {
    log.Print(err)
    return nil, err
  }

  defer resp.Body.Close()

  body, err := ioutil.ReadAll(resp.Body)
  log.Print(body)
  log.Print(err)
  if err != nil {
    log.Print(err)
    return nil, err
  }
  log.Print(string(body))
	return &metrics.MetricsResponse{Data: string(body)}, nil
}


// SayHello implements a interface defined by protobuf.
// func (s *MetricsServiceServer) SayHello(ctx context.Context, request *pb.HelloRequest) (*pb.HelloResponse, error) {
// 	customizedCounterMetric.WithLabelValues(request.Name).Inc()
// 	resp, err := http.Get("http://localhost:9604")
// 	if err != nil {
// 		log.Print(err)
// 		return nil, err
// 	}
//
// 	defer resp.Body.Close()
//
// 	 body, err := ioutil.ReadAll(resp.Body)
// 	 log.Print(body)
// 	 log.Print(err)
// 	 if err != nil {
// 			log.Print(err)
// 			return nil, err
// 	 }
// 	 log.Print(string(body))
// 	return &pb.HelloResponse{Message: string(body)}, err
// }


// NOTE: Graceful shutdown is missing. Don't use this demo in your production setup.
// func main() {
// 	// Listen an actual port.
// 	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", 9605))
// 	if err != nil {
// 		log.Fatalf("failed to listen: %v", err)
// 	}
// 	defer lis.Close()
//
// 	// Create a HTTP server for prometheus.
// 	httpServer := &http.Server{Handler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}), Addr: fmt.Sprintf("0.0.0.0:%d", 9604)}
//
// 	// Create a gRPC Server with gRPC interceptor.
// 	grpcServer := grpc.NewServer(
// 		grpc.StreamInterceptor(grpcMetrics.StreamServerInterceptor()),
// 		grpc.UnaryInterceptor(grpcMetrics.UnaryServerInterceptor()),
// 	)
//
// 	// Create a new api server.
// 	demoServer := newDemoServer()
//
// 	// Register your service.
// 	pb.RegisterMetricsServiceServer(grpcServer, demoServer)
// 	reflection.Register(grpcServer)
//
// 	// Initialize all metrics.
// 	grpcMetrics.InitializeMetrics(grpcServer)
//
// 	// Start your http server for prometheus.
// 	go func() {
// 		if err := httpServer.ListenAndServe(); err != nil {
// 			log.Fatal("Unable to start a http server.")
// 		}
// 	}()
//
// 	// Start your gRPC server.
// 	log.Fatal(grpcServer.Serve(lis))
// }
