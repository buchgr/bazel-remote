package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // Register pprof handlers with DefaultServeMux.
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	auth "github.com/abbot/go-http-auth"

	"github.com/buchgr/bazel-remote/cache/disk"

	"github.com/buchgr/bazel-remote/config"
	"github.com/buchgr/bazel-remote/server"
	"github.com/buchgr/bazel-remote/utils/flags"
	"github.com/buchgr/bazel-remote/utils/idle"
	"github.com/buchgr/bazel-remote/utils/rlimit"

	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	httpmetrics "github.com/slok/go-http-metrics/metrics/prometheus"
	middleware "github.com/slok/go-http-metrics/middleware"
	middlewarestd "github.com/slok/go-http-metrics/middleware/std"
	"github.com/urfave/cli/v2"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// gitCommit is the version stamp for the server. The value of this var
// is set through linker options.
var gitCommit string

func main() {
	log.SetFlags(config.LogFlags)

	maybeGitCommitMsg := ""
	if len(gitCommit) > 0 && gitCommit != "{STABLE_GIT_COMMIT}" {
		maybeGitCommitMsg = fmt.Sprintf(" from git commit %s", gitCommit)
	}
	log.Printf("bazel-remote built with %s%s.",
		runtime.Version(), maybeGitCommitMsg)

	app := cli.NewApp()

	cli.AppHelpTemplate = flags.Template
	cli.HelpPrinterCustom = flags.HelpPrinter
	// Force the use of cli.HelpPrinterCustom.
	app.ExtraInfo = func() map[string]string { return map[string]string{} }

	app.Flags = flags.GetCliFlags()
	app.Action = run

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal("bazel-remote terminated:", err)
	}
}

func run(ctx *cli.Context) error {
	c, err := config.Get(ctx)
	if err != nil {
		fmt.Fprintf(ctx.App.Writer, "%v\n\n", err)
		_ = cli.ShowAppHelp(ctx)
		return cli.Exit(err.Error(), 1)
	}

	if ctx.NArg() > 0 {
		fmt.Fprintf(ctx.App.Writer,
			"Error: bazel-remote does not take positional aguments\n")
		for i := 0; i < ctx.NArg(); i++ {
			fmt.Fprintf(ctx.App.Writer, "arg: %s\n", ctx.Args().Get(i))
		}
		fmt.Fprintf(ctx.App.Writer, "\n")

		_ = cli.ShowAppHelp(ctx)
		os.Exit(1)
	}

	rlimit.Raise()

	grpcSem := semaphore.NewWeighted(1)
	var grpcServer *grpc.Server

	httpSem := semaphore.NewWeighted(1)
	var httpServer *http.Server

	idleTimeoutChan := make(chan struct{}, 1)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigChan:
			log.Printf("Received signal: %s, attempting graceful shutdown", sig)
		case <-idleTimeoutChan:
			log.Println("Idle timeout reached, attempting graceful shutdown")
		}

		go func() {
			if !grpcSem.TryAcquire(1) {
				if grpcServer != nil {
					log.Println("Stopping gRPC server")
					grpcServer.GracefulStop()
					log.Println("gRPC server stopped")
				}
			}
		}()

		go func() {
			if !httpSem.TryAcquire(1) {
				if httpServer != nil {
					log.Println("Stopping HTTP server")
					err := httpServer.Shutdown(context.Background())
					if err != nil {
						log.Println("Error occurred while stopping HTTP server:", err)
					} else {
						log.Println("HTTP server stopped")
					}
				}
			}
		}()
	}()

	log.Println("Storage mode:", c.StorageMode)
	if c.StorageMode == "zstd" {
		log.Println("Zstandard implementation:", c.ZstdImplementation)
	}

	opts := []disk.Option{
		disk.WithStorageMode(c.StorageMode),
		disk.WithZstdImplementation(c.ZstdImplementation),
		disk.WithMaxBlobSize(c.MaxBlobSize),
		disk.WithProxyMaxBlobSize(c.MaxProxyBlobSize),
		disk.WithAccessLogger(c.AccessLogger),
	}
	if c.ProxyBackend != nil {
		opts = append(opts, disk.WithProxyBackend(c.ProxyBackend))
	}
	if c.EnableEndpointMetrics {
		opts = append(opts, disk.WithEndpointMetrics())
	}

	diskCache, err := disk.New(c.Dir, int64(c.MaxSize)*1024*1024*1024, opts...)
	if err != nil {
		log.Fatal(err)
	}
	diskCache.RegisterMetrics()

	servers := new(errgroup.Group)

	var htpasswdSecrets auth.SecretProvider

	authMode := "disabled"
	if c.HtpasswdFile != "" {
		authMode = "basic"
		htpasswdSecrets = auth.HtpasswdFileProvider(c.HtpasswdFile)
	} else if c.TLSCaFile != "" {
		authMode = "mTLS"
	}
	log.Println("Authentication:", authMode)

	if authMode != "disabled" {
		if c.AllowUnauthenticatedReads {
			log.Println("Access mode: authentication required for writes, unauthenticated reads allowed")
		} else {
			log.Println("Access mode: authentication required")
		}
	}

	var idleTimer *idle.Timer
	if c.IdleTimeout > 0 {
		idleTimer = idle.NewTimer(c.IdleTimeout, idleTimeoutChan)
	}

	acKeyManglingStatus := "disabled"
	if c.EnableACKeyInstanceMangling {
		acKeyManglingStatus = "enabled"
	}
	log.Println("Mangling non-empty instance names with AC keys:", acKeyManglingStatus)

	servers.Go(func() error {
		err := startHttpServer(c, &httpServer, htpasswdSecrets, idleTimer, httpSem, diskCache)
		if err != nil {
			log.Fatal("HTTP server returned fatal error:", err)
		}
		return nil
	})

	if c.GRPCAddress != "none" {
		servers.Go(func() error {
			err := startGrpcServer(c, &grpcServer, htpasswdSecrets, idleTimer, grpcSem, diskCache)
			if err != nil {
				log.Fatal("gRPC server returned fatal error:", err)
			}
			return nil
		})
	}

	if c.ProfileAddress != "" {
		go func() {
			// Allow access to /debug/pprof/ URLs.
			log.Printf("Starting HTTP server for profiling on address %s",
				c.ProfileAddress)
			log.Fatal(`Failed to listen on address: "`, c.ProfileAddress,
				`": `, http.ListenAndServe(c.ProfileAddress, nil))
		}()
	}

	if idleTimer != nil {
		log.Printf("Starting idle timer with value %v", c.IdleTimeout)
		idleTimer.Start()
	}

	return servers.Wait()
}

func startHttpServer(c *config.Config, httpServer **http.Server,
	htpasswdSecrets auth.SecretProvider, idleTimer *idle.Timer,
	httpSem *semaphore.Weighted, diskCache disk.Cache) error {

	mux := http.NewServeMux()
	*httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  c.HTTPReadTimeout,
		TLSConfig:    c.TLSConfig,
		WriteTimeout: c.HTTPWriteTimeout,
	}

	checkClientCertForReads := c.TLSCaFile != "" && !c.AllowUnauthenticatedReads
	checkClientCertForWrites := c.TLSCaFile != ""
	validateAC := !c.DisableHTTPACValidation
	h := server.NewHTTPCache(diskCache, c.AccessLogger, c.ErrorLogger, validateAC,
		c.EnableACKeyInstanceMangling, checkClientCertForReads, checkClientCertForWrites, gitCommit)

	cacheHandler := h.CacheHandler
	var basicAuthenticator auth.BasicAuth
	if c.HtpasswdFile != "" {
		if c.AllowUnauthenticatedReads {
			cacheHandler = unauthenticatedReadWrapper(cacheHandler, htpasswdSecrets, c.HTTPAddress)
		} else {
			basicAuthenticator = auth.BasicAuth{Realm: c.HTTPAddress, Secrets: htpasswdSecrets}
			cacheHandler = basicAuthWrapper(cacheHandler, &basicAuthenticator)
		}
	}

	if c.IdleTimeout > 0 {
		cacheHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			idleTimer.ResetTimer()
			cacheHandler(w, r)
		})
	}

	var statusHandler http.HandlerFunc = h.StatusPageHandler

	if !c.AllowUnauthenticatedReads {
		if c.TLSCaFile != "" {
			statusHandler = h.VerifyClientCertHandler(statusHandler).ServeHTTP
		} else if c.HtpasswdFile != "" {
			statusHandler = basicAuthWrapper(statusHandler, &basicAuthenticator)
		}
	}

	if c.EnableEndpointMetrics {
		metricsMdlw := middleware.New(middleware.Config{
			Recorder: httpmetrics.NewRecorder(httpmetrics.Config{
				DurationBuckets: c.MetricsDurationBuckets,
			}),
		})

		middlewareHandler := middlewarestd.Handler("metrics", metricsMdlw, promhttp.Handler())
		if !c.AllowUnauthenticatedReads {
			if c.TLSCaFile != "" {
				middlewareHandler = h.VerifyClientCertHandler(middlewareHandler)
			} else if c.HtpasswdFile != "" {
				middlewareHandler = basicAuthWrapper(middlewareHandler.ServeHTTP, &basicAuthenticator)
			}
		}
		mux.Handle("/metrics", middlewareHandler)

		statusHandler = middlewarestd.Handler("status", metricsMdlw, http.HandlerFunc(h.StatusPageHandler)).ServeHTTP

		ch := cacheHandler // Avoid an infinite loop in the closure below.
		cacheHandler = func(w http.ResponseWriter, r *http.Request) {
			middlewarestd.Handler(r.Method, metricsMdlw, http.HandlerFunc(ch)).ServeHTTP(w, r)
		}
	}

	mux.HandleFunc("/status", statusHandler)
	mux.HandleFunc("/", cacheHandler)

	var ln net.Listener
	var err error
	if strings.HasPrefix(c.HTTPAddress, "unix://") {
		ln, err = net.Listen("unix", c.HTTPAddress[len("unix://"):])
	} else {
		ln, err = net.Listen("tcp", c.HTTPAddress)
	}
	if err != nil {
		log.Fatal(`Failed to listen on address: "`, c.HTTPAddress, `": `, err)
	}

	validateStatus := "disabled"
	if validateAC {
		validateStatus = "enabled"
	}
	log.Println("HTTP AC validation:", validateStatus)

	if len(c.TLSCertFile) > 0 && len(c.TLSKeyFile) > 0 {
		if !httpSem.TryAcquire(1) {
			log.Println("bazel-remote is shutting down, not starting HTTPS server")
			return nil
		}

		log.Printf("Starting HTTPS server on address %s", c.HTTPAddress)
		err = (*httpServer).ServeTLS(ln, c.TLSCertFile, c.TLSKeyFile)
		if err == http.ErrServerClosed {
			log.Println("HTTPS server stopped")
			return nil
		}

		return err
	}

	if !httpSem.TryAcquire(1) {
		log.Println("bazel-remote is shutting down, not starting HTTP server")
		return nil
	}

	log.Printf("Starting HTTP server on address %s", c.HTTPAddress)
	err = (*httpServer).Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}

	return err
}

func startGrpcServer(c *config.Config, grpcServer **grpc.Server,
	htpasswdSecrets auth.SecretProvider, idleTimer *idle.Timer,
	grpcSem *semaphore.Weighted, diskCache disk.Cache) error {

	opts := []grpc.ServerOption{}
	streamInterceptors := []grpc.StreamServerInterceptor{}
	unaryInterceptors := []grpc.UnaryServerInterceptor{}

	if c.EnableEndpointMetrics {
		streamInterceptors = append(streamInterceptors, grpc_prometheus.StreamServerInterceptor)
		unaryInterceptors = append(unaryInterceptors, grpc_prometheus.UnaryServerInterceptor)
		grpc_prometheus.EnableHandlingTimeHistogram(grpc_prometheus.WithHistogramBuckets(c.MetricsDurationBuckets))
	}

	if c.TLSConfig != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(c.TLSConfig)))

		if c.TLSCaFile != "" {
			streamInterceptors = append(streamInterceptors,
				server.GRPCmTLSStreamServerInterceptor(c.AllowUnauthenticatedReads))
			unaryInterceptors = append(unaryInterceptors,
				server.GRPCmTLSUnaryServerInterceptor(c.AllowUnauthenticatedReads))
		}
	}

	if htpasswdSecrets != nil {
		gba := server.NewGrpcBasicAuth(htpasswdSecrets, c.AllowUnauthenticatedReads)
		streamInterceptors = append(streamInterceptors, gba.StreamServerInterceptor)
		unaryInterceptors = append(unaryInterceptors, gba.UnaryServerInterceptor)
	}

	if idleTimer != nil {
		it := server.NewGrpcIdleTimer(idleTimer)
		streamInterceptors = append(streamInterceptors, it.StreamServerInterceptor)
		unaryInterceptors = append(unaryInterceptors, it.UnaryServerInterceptor)
	}

	opts = append(opts, grpc.ChainStreamInterceptor(streamInterceptors...))
	opts = append(opts, grpc.ChainUnaryInterceptor(unaryInterceptors...))

	validateAC := !c.DisableGRPCACDepsCheck
	validateStatus := "disabled"
	if validateAC {
		validateStatus = "enabled"
	}
	log.Println("gRPC AC dependency checks:", validateStatus)

	enableRemoteAssetAPI := c.ExperimentalRemoteAssetAPI
	remoteAssetStatus := "disabled"
	if enableRemoteAssetAPI {
		remoteAssetStatus = "enabled"
	}
	log.Println("experimental gRPC remote asset API:", remoteAssetStatus)

	network := "tcp"
	addr := c.GRPCAddress
	if strings.HasPrefix(c.GRPCAddress, "unix://") {
		network = "unix"
		addr = c.GRPCAddress[len("unix://"):]
	}

	*grpcServer = grpc.NewServer(opts...)

	if !grpcSem.TryAcquire(1) {
		log.Println("bazel-remote is shutting down, not starting gRPC server")
		return nil
	}

	log.Println("Starting gRPC server on address", addr)

	return server.ListenAndServeGRPC(*grpcServer,
		network, addr,
		validateAC,
		c.EnableACKeyInstanceMangling,
		enableRemoteAssetAPI,
		diskCache, c.AccessLogger, c.ErrorLogger)
}

// A http.HandlerFunc wrapper which requires successful basic
// authentication for all requests.
func basicAuthWrapper(handler http.HandlerFunc, authenticator *auth.BasicAuth) http.HandlerFunc {
	return auth.JustCheck(authenticator, handler)
}

// A http.HandlerFunc wrapper which requires successful basic
// authentication for write requests, but allows unauthenticated
// read requests.
func unauthenticatedReadWrapper(handler http.HandlerFunc, secrets auth.SecretProvider, addr string) http.HandlerFunc {
	authenticator := &auth.BasicAuth{Realm: addr, Secrets: secrets}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			handler(w, r)
			return
		}

		if authenticator.CheckAuth(r) != "" {
			handler(w, r)
			return
		}

		http.Error(w, "Authorization required", http.StatusUnauthorized)
		// TODO: pass in a logger so we can log this event?
	}
}
