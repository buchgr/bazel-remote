package disk

import (
	"context"
	"io"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/hashing"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"

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

func (m *metricsDecorator) Get(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64, offset int64) (io.ReadCloser, int64, error) {
	rc, size, err := m.diskCache.Get(ctx, kind, hasher, hash, size, offset)
	if err != nil {
		return rc, size, err
	}

	lbls := prometheus.Labels{"method": getMethod, "kind": kind.String()}
	if rc != nil {
		lbls["status"] = hitStatus
	} else {
		lbls["status"] = missStatus
	}
	m.counter.With(lbls).Inc()

	return rc, size, nil
}

func (m *metricsDecorator) GetValidatedActionResult(ctx context.Context, hasher hashing.Hasher, hash string) (*pb.ActionResult, []byte, error) {
	ar, data, err := m.diskCache.GetValidatedActionResult(ctx, hasher, hash)
	if err != nil {
		return ar, data, err
	}

	lbls := prometheus.Labels{"method": getMethod, "kind": acKind}
	if ar != nil {
		lbls["status"] = hitStatus
	} else {
		lbls["status"] = missStatus
	}
	m.counter.With(lbls).Inc()

	return ar, data, err
}

func (m *metricsDecorator) GetZstd(ctx context.Context, hasher hashing.Hasher, hash string, size int64, offset int64) (io.ReadCloser, int64, error) {
	rc, size, err := m.diskCache.GetZstd(ctx, hasher, hash, size, offset)
	if err != nil {
		return rc, size, err
	}

	lbls := prometheus.Labels{
		"method": getMethod,
		"kind":   "cas",
	}
	if rc != nil {
		lbls["status"] = hitStatus
	} else {
		lbls["status"] = missStatus
	}
	m.counter.With(lbls).Inc()

	return rc, size, nil
}

func (m *metricsDecorator) Contains(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64) (bool, int64) {
	ok, size := m.diskCache.Contains(ctx, kind, hasher, hash, size)

	lbls := prometheus.Labels{"method": containsMethod, "kind": kind.String()}
	if ok {
		lbls["status"] = hitStatus
	} else {
		lbls["status"] = missStatus
	}
	m.counter.With(lbls).Inc()

	return ok, size
}

func (m *metricsDecorator) FindMissingCasBlobs(ctx context.Context, hasher hashing.Hasher, blobs []*pb.Digest) ([]*pb.Digest, error) {
	numLooking := len(blobs)
	digests, err := m.diskCache.FindMissingCasBlobs(ctx, hasher, blobs)
	if err != nil {
		return digests, err
	}

	numMissing := len(digests)

	numFound := numLooking - numMissing

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

	return digests, nil
}
