package disk

import (
	"context"
	"io"

	"github.com/buchgr/bazel-remote/cache"
	"google.golang.org/grpc/metadata"
	"net/http"

	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"

	"github.com/prometheus/client_golang/prometheus"
)

type metricsDecorator struct {
	counter *prometheus.CounterVec
	*diskCache
	categories map[string][]string
}

const (
	hitStatus   = "hit"
	missStatus  = "miss"
	emptyStatus = ""

	containsMethod = "contains"
	getMethod      = "get"
	putMethod      = "put"

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
	if err != nil {
		return rc, size, err
	}

	lbls := prometheus.Labels{"method": getMethod, "kind": kind.String()}
	if rc != nil {
		lbls["status"] = hitStatus
	} else {
		lbls["status"] = missStatus
	}
	m.addCategoryLabels(ctx, lbls)
	m.counter.With(lbls).Inc()

	return rc, size, nil
}

func (m *metricsDecorator) GetValidatedActionResult(ctx context.Context, hash string) (*pb.ActionResult, []byte, error) {
	ar, data, err := m.diskCache.GetValidatedActionResult(ctx, hash)
	if err != nil {
		return ar, data, err
	}

	lbls := prometheus.Labels{"method": getMethod, "kind": acKind}
	if ar != nil {
		lbls["status"] = hitStatus
	} else {
		lbls["status"] = missStatus
	}
	m.addCategoryLabels(ctx, lbls)
	m.counter.With(lbls).Inc()

	return ar, data, err
}

func (m *metricsDecorator) GetZstd(ctx context.Context, hash string, size int64, offset int64) (io.ReadCloser, int64, error) {
	rc, size, err := m.diskCache.GetZstd(ctx, hash, size, offset)
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
	m.addCategoryLabels(ctx, lbls)
	m.counter.With(lbls).Inc()

	return rc, size, nil
}

func (m *metricsDecorator) Contains(ctx context.Context, kind cache.EntryKind, hash string, size int64) (bool, int64) {
	ok, size := m.diskCache.Contains(ctx, kind, hash, size)

	lbls := prometheus.Labels{"method": containsMethod, "kind": kind.String()}
	if ok {
		lbls["status"] = hitStatus
	} else {
		lbls["status"] = missStatus
	}
	m.addCategoryLabels(ctx, lbls)
	m.counter.With(lbls).Inc()

	return ok, size
}

func (m *metricsDecorator) FindMissingCasBlobs(ctx context.Context, blobs []*pb.Digest) ([]*pb.Digest, error) {
	numLooking := len(blobs)
	digests, err := m.diskCache.FindMissingCasBlobs(ctx, blobs)
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
	m.addCategoryLabels(ctx, hitLabels)
	hits := m.counter.With(hitLabels)

	missLabels := prometheus.Labels{
		"method": containsMethod,
		"kind":   "cas",
		"status": missStatus,
	}
	m.addCategoryLabels(ctx, missLabels)
	misses := m.counter.With(missLabels)

	hits.Add(float64(numFound))
	misses.Add(float64(numMissing))

	return digests, nil
}

func (m *metricsDecorator) Put(ctx context.Context, kind cache.EntryKind, hash string, size int64, r io.Reader) error {
	err := m.diskCache.Put(ctx, kind, hash, size, r)
	if err != nil {
		return err
	}

	lbls := prometheus.Labels{"method": putMethod, "kind": kind.String(), "status": emptyStatus}
	m.addCategoryLabels(ctx, lbls)
	m.counter.With(lbls).Inc()

	return nil
}

// Update prometheus labels based on HTTP and gRPC headers available via the context.
func (m *metricsDecorator) addCategoryLabels(ctx context.Context, labels prometheus.Labels) {

	if len(m.categories) == 0 {
		return
	}

	httpHeaders := getHttpHeaders(ctx)
	var grpcHeaders metadata.MD
	if httpHeaders == nil {
		grpcHeaders = getGrpcHeaders(ctx)
	}

	for categoryNameLowerCase, allowedValues := range m.categories {
		// Lower case is canonical for gRPC headers and convention for prometheus.
		var headerValue string = ""
		if grpcHeaders != nil {
			grpcHeaderValues := grpcHeaders[categoryNameLowerCase]
			if len(grpcHeaderValues) > 0 {
				// Pick the first header with matching name if multiple headers with same name
				headerValue = grpcHeaderValues[0]
			}
		} else if httpHeaders != nil {
			headerValue = httpHeaders.Get(categoryNameLowerCase)
		}
		if len(headerValue) == 0 {
			labels[categoryNameLowerCase] = ""
		} else if contains(allowedValues, headerValue) {
			labels[categoryNameLowerCase] = headerValue
		} else {
			labels[categoryNameLowerCase] = "other"
		}
	}
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

type httpHeadersContextKey struct{}

// Creates a context copy with HTTP headers attached.
func ContextWithHttpHeaders(ctx context.Context, headers *http.Header) context.Context {
	return context.WithValue(ctx, httpHeadersContextKey{}, headers)
}

// Retrieves HTTP headers from context. Minimizes type safety issues.
func getHttpHeaders(ctx context.Context) *http.Header {
	headers, ok := ctx.Value(httpHeadersContextKey{}).(*http.Header)
	if !ok {
		return nil
	}
	return headers
}

func getGrpcHeaders(ctx context.Context) metadata.MD {
	grpcHeaders, _ := metadata.FromIncomingContext(ctx)
	return grpcHeaders
}
