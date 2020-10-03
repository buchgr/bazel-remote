package prometheus

import (
	"net/http"

	"github.com/buchgr/bazel-remote/metric"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	httpmetrics "github.com/slok/go-http-metrics/metrics/prometheus"
	"github.com/slok/go-http-metrics/middleware"
	middlewarestd "github.com/slok/go-http-metrics/middleware/std"
)

// durationBuckets is the buckets used for Prometheus histograms in seconds.
var durationBuckets = []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320}

// map metric names to their help message
var help = map[string]string{
	"bazel_remote_disk_cache_hits":                    "The total number of disk backend cache hits",
	"bazel_remote_disk_cache_misses":                  "The total number of disk backend cache misses",
	"bazel_remote_disk_cache_size_bytes":              "The current number of bytes in the disk backend",
	"bazel_remote_disk_cache_evicted_bytes_total":     "The total number of bytes evicted from disk backend, due to full cache",
	"bazel_remote_disk_cache_overwritten_bytes_total": "The total number of bytes removed from disk backend, due to put of already existing key",
	"bazel_remote_http_cache_hits":                    "The total number of HTTP backend cache hits",
	"bazel_remote_http_cache_misses":                  "The total number of HTTP backend cache misses",
	"bazel_remote_s3_cache_hits":                      "The total number of s3 backend cache hits",
	"bazel_remote_s3_cache_misses":                    "The total number of s3 backend cache misses",
}

// NewCollector returns a prometheus backed collector
func NewCollector() metric.Collector {
	return &collector{}
}

// WrapEndpoints attaches the prometheus metrics endpoints to a mux
func WrapEndpoints(mux *http.ServeMux, cache http.HandlerFunc, status http.HandlerFunc) {
	metricsMdlw := middleware.New(middleware.Config{
		Recorder: httpmetrics.NewRecorder(httpmetrics.Config{
			DurationBuckets: durationBuckets,
		}),
	})
	mux.Handle("/metrics", middlewarestd.Handler("metrics", metricsMdlw, promhttp.Handler()))
	mux.Handle("/status", middlewarestd.Handler("status", metricsMdlw, http.HandlerFunc(status)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		middlewarestd.Handler(r.Method, metricsMdlw, http.HandlerFunc(cache)).ServeHTTP(w, r)
	})
}

type collector struct{}

func (c *collector) NewCounter(name string) metric.Counter {
	return promauto.NewCounter(prometheus.CounterOpts{
		Name: name,
		Help: help[name],
	})
}

func (c *collector) NewGuage(name string) metric.Gauge {
	return promauto.NewGauge(prometheus.GaugeOpts{
		Name: "bazel_remote_disk_cache_size_bytes",
		Help: help[name],
	})
}
