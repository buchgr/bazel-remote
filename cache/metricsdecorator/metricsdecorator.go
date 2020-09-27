package metricsdecorator

// This is a decorator for any implementation of the cache.BlobAcStore interface.
// It adds prometheus metrics for the cache requests.
//
// The decorator can report cache miss if AC is found but referenced CAS entries are missing.
// That is possible since metricsdecorator supports GetValidatedActionResult in the
// cache.BlobAcStore interface.
//
// TODO Consider allow using a metricsdecorator also for pure cache.BlobStore interfaces,
//      in order to replace the current prometheus counters in the proxies? That would
//      probably require better support for non AC requests in metricsdecorator and configurable
//      counter name.
import (
	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"io"
	"strings"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
)

type metrics struct {
	categoryValues      map[string]map[string]struct{}
	counterIncomingReqs *prometheus.CounterVec
	parent              cache.BlobAcStore
}

const statusOK = "ok"
const statusNotFound = "notFound"
const statusError = "error"

const methodGet = "get"
const methodPut = "put"
const methodContains = "contains"

// TODO add test cases for this file

func NewMetricsDecorator(config *config.Metrics, parent cache.BlobAcStore) cache.BlobAcStore {

	labels := []string{"method", "status", "kind"}
	categoryValues := make(map[string]map[string]struct{})

	if config != nil && config.Categories != nil {
		for categoryName, allowedValues := range config.Categories {
			// Normalize to lower case since canonical for gRPC headers
			// and convention for prometheus.
			categoryName := strings.ToLower(categoryName)

			// Store allowed category values as set for efficient access
			allowedValuesSet := make(map[string]struct{})
			for _, categoryValue := range allowedValues {
				allowedValuesSet[categoryValue] = struct{}{}
			}
			categoryValues[categoryName] = allowedValuesSet

			// Construct a prometheus label for each category.
			// Prometheus does not allow changing set of
			// labels until next time bazel-remote is
			// restarted.
			labels = append(labels, categoryName)
		}
	}

	// For now we only count AC requests, and only the most common status codes,
	// becuse:
	//
	//  - No identified use case for others.
	//  - Limit number of prometheus time series (if many configured categories).
	//  - Reduce performance overhead of counters (if many configured categories).
	//
	// But the naming, and the labels, of the counter, are generic to allow
	// counting additional requests types or status codes in the future. Without
	// having to rename the counter and get issues with non continous history of
	// metrics.

	counterIncomingReqs := promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bazel_remote_incoming_requests_total",
			Help: "The number of incoming HTTP and gRPC request. Currently only AC requests",
		},
		labels)

	m := &metrics{
		categoryValues:      categoryValues,
		counterIncomingReqs: counterIncomingReqs,
		parent:              parent,
	}
	return m
}

func (m *metrics) Put(kind cache.EntryKind, hash string, size int64, r io.Reader, context cache.RequestContext) error {
	err := m.parent.Put(kind, hash, size, r, context)

	if kind == cache.AC {
		var status string
		if err != nil {
			status = statusError
		} else {
			status = statusOK
		}
		m.incrementRequests(kind, methodPut, status, context)
	}

	return err
}

func (m *metrics) Get(kind cache.EntryKind, hash string, size int64, context cache.RequestContext) (io.ReadCloser, int64, error) {
	rc, sizeBytes, err := m.parent.Get(kind, hash, size, context)

	if kind == cache.AC {
		var status string
		if err != nil {
			status = statusError
		} else if rc == nil {
			status = statusNotFound
		} else {
			status = statusOK
		}
		m.incrementRequests(kind, methodGet, status, context)
	}

	return rc, sizeBytes, err
}

func (m *metrics) Contains(kind cache.EntryKind, hash string, size int64, context cache.RequestContext) (bool, int64) {
	ok, sizeBytes := m.parent.Contains(kind, hash, size, context)

	if kind == cache.AC {
		var status string
		if ok {
			status = statusOK
		} else {
			status = statusNotFound
		}
		m.incrementRequests(kind, methodContains, status, context)
	}

	return ok, sizeBytes
}

func (m *metrics) GetValidatedActionResult(hash string, context cache.RequestContext) (*pb.ActionResult, []byte, error) {
	ac, data, err := m.parent.GetValidatedActionResult(hash, context)

	var status string
	if err != nil {
		status = statusError
	} else if ac == nil {
		status = statusNotFound
	} else {
		status = statusOK
	}
	m.incrementRequests(cache.AC, methodGet, status, context)

	return ac, data, err
}

func getLabelValueFromHeaderValues(headerValues []string, allowedValues map[string]struct{}) string {
	if len(headerValues) == 0 {
		return "" // No header for this label
	}
	for _, headerValue := range headerValues {
		// Prometheus only allows one value per label.
		// Pick the first allowed header value we find.
		if _, ok := allowedValues[headerValue]; ok {
			return headerValue
		}
	}

	// The values found in the header has not been listed in
	// the configuration file. Represent them as "other".
	//
	// Listening allowed values is an attempt to avoid polluting
	// prometheus with too many different time series.
	//
	// https://prometheus.io/docs/practices/naming/ warns about:
	//
	//   "CAUTION: Remember that every unique combination of key-value
	//   label pairs represents a new time series, which can dramatically
	//   increase the amount of data stored. Do not use labels to store
	//   dimensions with high cardinality (many different label values),
	//   such as user IDs, email addresses, or other unbounded sets of
	//   values."
	//
	// It would have been nice if bazel-remote could reload the set
	// of allowed values from updated configuration file, by
	// SIGHUP signal instead of having to restart bazel-remote.
	return "other"
}

func (m *metrics) incrementRequests(kind cache.EntryKind, method string, status string, reqCtx cache.RequestContext) {
	labels := make(prometheus.Labels)
	labels["method"] = method
	labels["status"] = status
	labels["kind"] = kind.String()

	for labelName := range m.categoryValues {
		labels[labelName] = getLabelValueFromHeaderValues(reqCtx.GetHeader(labelName), m.categoryValues[labelName])
	}

	m.counterIncomingReqs.With(labels).Inc()
}
