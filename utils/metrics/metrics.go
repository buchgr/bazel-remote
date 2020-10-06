package metrics

import (
	"github.com/buchgr/bazel-remote/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"strings"
)

// TODO Add test cases for this file

type Method int
type Status int
type Kind int
type Protocol int

const (
	METHOD_GET Method = iota
	METHOD_HEAD
	METHOD_PUT
	METHOD_OTHER
)

const (
	OK Status = iota
	NOT_FOUND
	OTHER_STATUS
)

const (
	AC Kind = iota
	CAS
)

const (
	HTTP Protocol = iota
	GRPC
)

func (e Method) String() string {
	// Actually HTTP names, but can be conceptually mapped also to GRPC protocol.
	if e == METHOD_GET {
		return "GET"
	}
	if e == METHOD_HEAD {
		return "HEAD"
	}
	if e == METHOD_PUT {
		return "PUT"
	}
	return "other"
}

func (e Status) String() string {
	// Names that works for both HTTP and GRPC, instead of HTTP or GRPC specific codes.
	if e == OK {
		return "OK"
	}
	if e == NOT_FOUND {
		return "NotFound"
	}
	return "other"
}

func (e Kind) String() string {
	if e == AC {
		return "AC"
	}
	if e == CAS {
		return "CAS"
	}
	return "other"
}

type Metrics interface {
	// TODO Document interface
	IncomingRequestCompleted(kind Kind, method Method, status Status, headers map[string][]string, protocol Protocol)
}

type metrics struct {
	categoryValues               map[string]map[string]struct{}
	counterIncomingCompletedReqs *prometheus.CounterVec
}

func NewMetrics(config *config.Metrics) Metrics {

	labels := []string{"method", "status", "kind"}
	categoryValues := make(map[string]map[string]struct{})

	if config != nil && config.Categories != nil {
		for categoryName, whiteListedValues := range config.Categories {
			// Normalize to lower case since canonical for gRPC headers
			// and convention for prometheus.
			categoryName := strings.ToLower(categoryName)

			// Store white listed category values as set for efficient access
			whiteListedSet := make(map[string]struct{})
			for _, categoryValue := range whiteListedValues {
				whiteListedSet[categoryValue] = struct{}{}
			}
			categoryValues[categoryName] = whiteListedSet

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
	//  - Would otherwise require injecting invocations in more places.
	//
	// But the naming, and the labels, of the counter, are generic to allow
	// counting additional requests types or status codes in the future. Without
	// having to rename the counter and get issues with non continous history of
	// metrics.

	counterIncomingCompletedReqs := promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "bazel_remote_incoming_requests_completed_total",
			Help: "The number of incoming HTTP and gRPC request. Currently only AC requests",
		},
		labels)

	m := &metrics{
		categoryValues:               categoryValues,
		counterIncomingCompletedReqs: counterIncomingCompletedReqs,
	}
	return m
}

func getLabelValueFromHeaderValues(headerValues []string, whiteListedValues map[string]struct{}) string {
	for _, headerValue := range headerValues {
		// Prometheus only allows one value per label.
		// Pick the first white listed header value we find.
		if _, ok := whiteListedValues[headerValue]; ok {
			return headerValue
		}
	}

	// The values found in the header has not been white listed in
	// the configuration file. Represent them as "other".
	//
	// The white listening is an attempt to avoid polluting
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
	// of white listed values from updated configuration file, by
	// SIGHUP signal instead of having to restart bazel-remote.
	return "other"
}

func (m *metrics) IncomingRequestCompleted(kind Kind, method Method, status Status, headers map[string][]string, protocol Protocol) {
	labels := make(prometheus.Labels)
	labels["method"] = method.String()
	labels["status"] = status.String()
	labels["kind"] = kind.String()
	for labelName := range m.categoryValues {
		// The canonical form of gRPC and HTTP/2 headers is lowercase "category"
		headerName := labelName
		if protocol == HTTP {
			// but the golang http library is normalizing HTTP/1.1 headers as "Category".
			headerName = strings.Title(headerName)
		}
		if headerValues, ok := headers[headerName]; ok {
			labels[labelName] = getLabelValueFromHeaderValues(headerValues, m.categoryValues[labelName])
		} else {
			labels[labelName] = "" // no header for this label
		}
	}
	m.counterIncomingCompletedReqs.With(labels).Inc()
}
