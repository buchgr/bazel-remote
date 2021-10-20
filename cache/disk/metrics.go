package disk

import (
	"context"
	"io"

	"github.com/buchgr/bazel-remote/cache"

	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"

	"github.com/prometheus/client_golang/prometheus"
)

type metricsDecorator struct {
	counter *prometheus.CounterVec
	*diskCache
}

const (
	hitStatus  = "hit"
	missStatus = "miss"

	containsMethod = "contains"
	getMethod      = "get"
	//putMethod      = "put"

	acKind  = "ac" // This must be lowercase to match cache.EntryKind.String()
	casKind = "cas"
	rawKind = "raw"
)

func (m *metricsDecorator) RegisterMetrics() {
	prometheus.MustRegister(m.counter)
	m.diskCache.RegisterMetrics()
}

func (m *metricsDecorator) Get(ctx context.Context, kind cache.EntryKind, hash string, size int64, offset int64) (io.ReadCloser, int64, error) {
	rc, size, err := m.diskCache.Get(ctx, kind, hash, size, offset)

	lbls := prometheus.Labels{"method": getMethod, "kind": kind.String()}
	if rc != nil {
		lbls["status"] = hitStatus
	} else if err == nil {
		lbls["status"] = missStatus
	}
	m.counter.With(lbls).Inc()

	return rc, size, err
}

func (m *metricsDecorator) GetValidatedActionResult(ctx context.Context, hash string) (*pb.ActionResult, []byte, error) {
	ar, data, err := m.diskCache.GetValidatedActionResult(ctx, hash)

	lbls := prometheus.Labels{"method": getMethod, "kind": acKind}
	if ar != nil {
		lbls["status"] = hitStatus
	} else if err == nil {
		lbls["status"] = missStatus
	}
	m.counter.With(lbls).Inc()

	return ar, data, err
}

func (m *metricsDecorator) GetZstd(ctx context.Context, hash string, size int64, offset int64) (io.ReadCloser, int64, error) {
	rc, size, err := m.diskCache.GetZstd(ctx, hash, size, offset)

	lbls := prometheus.Labels{
		"method": getMethod,
		"kind":   "cas",
	}
	if rc != nil {
		lbls["status"] = hitStatus
	} else if err == nil {
		lbls["status"] = missStatus
	}
	m.counter.With(lbls).Inc()

	return rc, size, err
}

func (m *metricsDecorator) Contains(ctx context.Context, kind cache.EntryKind, hash string, size int64) (bool, int64) {
	ok, size := m.diskCache.Contains(ctx, kind, hash, size)

	lbls := prometheus.Labels{"method": containsMethod, "kind": kind.String()}
	if ok {
		lbls["status"] = hitStatus
	} else {
		lbls["status"] = missStatus
	}
	m.counter.With(lbls).Inc()

	return ok, size
}

func (m *metricsDecorator) FindMissingCasBlobs(ctx context.Context, blobs []*pb.Digest) ([]*pb.Digest, error) {
	numLooking := len(blobs)
	digests, err := m.diskCache.FindMissingCasBlobs(ctx, blobs)
	numFound := len(digests)

	numMissing := numLooking - numFound

	hitLabels := prometheus.Labels{
		"method": containsMethod,
		"kind":   "cas",
		"status": hitStatus,
	}
	hits := m.counter.With(hitLabels)

	missLabels := prometheus.Labels{
		"method": containsMethod,
		"kind":   "cas",
		"status": missStatus,
	}
	misses := m.counter.With(missLabels)

	hits.Add(float64(numFound))
	misses.Add(float64(numMissing))

	return digests, err
}
