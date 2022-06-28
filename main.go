package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof" // Register pprof handlers with DefaultServeMux.
	"os"
	"runtime"
	"strings"

	auth "github.com/abbot/go-http-auth"

	"github.com/buchgr/bazel-remote/cache"
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

	validateAC := !c.DisableHTTPACValidation

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

	mux := http.NewServeMux()
	httpServer := &http.Server{
		Handler:      mux,
		ReadTimeout:  c.HTTPReadTimeout,
		TLSConfig:    c.TLSConfig,
		WriteTimeout: c.HTTPWriteTimeout,
	}

	checkClientCertForWrites := c.TLSCaFile != "" && c.AllowUnauthenticatedReads
	h := server.NewHTTPCache(diskCache, c.AccessLogger, c.ErrorLogger, validateAC,
		c.EnableACKeyInstanceMangling, checkClientCertForWrites, gitCommit)

	var htpasswdSecrets auth.SecretProvider
	authMode := "disabled"
	cacheHandler := h.CacheHandler
	if c.HtpasswdFile != "" {
		authMode = "basic"
		htpasswdSecrets = auth.HtpasswdFileProvider(c.HtpasswdFile)
		if c.AllowUnauthenticatedReads {
			cacheHandler = unauthenticatedReadWrapper(cacheHandler, htpasswdSecrets, c.HTTPAddress)
		} else {
			cacheHandler = authWrapper(cacheHandler, htpasswdSecrets, c.HTTPAddress)
		}
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
		idleTimer = idle.NewTimer(c.IdleTimeout)
		cacheHandler = wrapIdleHandler(cacheHandler, idleTimer, c.AccessLogger, httpServer)
	}

	acKeyManglingStatus := "disabled"
	if c.EnableACKeyInstanceMangling {
		acKeyManglingStatus = "enabled"
	}
	log.Println("Mangling non-empty instance names with AC keys:", acKeyManglingStatus)

	if c.EnableEndpointMetrics {
		metricsMdlw := middleware.New(middleware.Config{
			Recorder: httpmetrics.NewRecorder(httpmetrics.Config{
				DurationBuckets: c.MetricsDurationBuckets,
			}),
		})
		mux.Handle("/metrics", middlewarestd.Handler("metrics", metricsMdlw, promhttp.Handler()))
		mux.Handle("/status", middlewarestd.Handler("status", metricsMdlw, http.HandlerFunc(h.StatusPageHandler)))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			middlewarestd.Handler(r.Method, metricsMdlw, http.HandlerFunc(cacheHandler)).ServeHTTP(w, r)
		})
	} else {
		mux.HandleFunc("/status", h.StatusPageHandler)
		mux.HandleFunc("/", cacheHandler)
	}

	if c.GRPCAddress != "none" {
		go func() {
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

			log.Printf("Starting gRPC server on address %s", addr)

			err3 := server.ListenAndServeGRPC(
				network, addr, opts,
				validateAC,
				c.EnableACKeyInstanceMangling,
				enableRemoteAssetAPI,
				diskCache, c.AccessLogger, c.ErrorLogger)
			if err3 != nil {
				log.Fatal(err3)
			}
		}()
	}

	if c.ProfileAddress != "" {
		go func() {
			// Allow access to /debug/pprof/ URLs.
			log.Printf("Starting HTTP server for profiling on address %s",
				c.ProfileAddress)
			log.Fatal(`Failed to listen on address: "`, c.ProfileAddress, `": `, http.ListenAndServe(c.ProfileAddress, nil))
		}()
	}

	validateStatus := "disabled"
	if validateAC {
		validateStatus = "enabled"
	}

	var ln net.Listener
	if strings.HasPrefix(c.HTTPAddress, "unix://") {
		ln, err = net.Listen("unix", c.HTTPAddress[len("unix://"):])
	} else {
		ln, err = net.Listen("tcp", c.HTTPAddress)
	}
	if err != nil {
		log.Fatal(`Failed to listen on address: "`, c.HTTPAddress, `": `, err)
	}

	if len(c.TLSCertFile) > 0 && len(c.TLSKeyFile) > 0 {
		log.Printf("Starting HTTPS server on address %s", httpServer.Addr)
		log.Println("HTTP AC validation:", validateStatus)
		return httpServer.ServeTLS(ln, c.TLSCertFile, c.TLSKeyFile)
	}

	if idleTimer != nil {
		log.Printf("Starting idle timer with value %v", c.IdleTimeout)
		idleTimer.Start()
	}

	log.Printf("Starting HTTP server on address %s", c.HTTPAddress)
	log.Println("HTTP AC validation:", validateStatus)
	return httpServer.Serve(ln)
}

func wrapIdleHandler(handler http.HandlerFunc, idleTimer *idle.Timer, accessLogger cache.Logger, httpServer *http.Server) http.HandlerFunc {

	tearDown := make(chan struct{})
	idleTimer.Register(tearDown)

	go func() {
		<-tearDown
		accessLogger.Printf("Shutting down after idle timeout")
		err := httpServer.Shutdown(context.Background())
		if err != nil {
			fmt.Printf("Error when shutting down http server: %s", err.Error())
		}
	}()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idleTimer.ResetTimer()
		handler(w, r)
	})
}

// A http.HandlerFunc wrapper which requires successful basic
// authentication for all requests.
func authWrapper(handler http.HandlerFunc, secrets auth.SecretProvider, addr string) http.HandlerFunc {
	authenticator := &auth.BasicAuth{Realm: addr, Secrets: secrets}
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
