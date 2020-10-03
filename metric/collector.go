package metric

// Counter is a standard metric counter
type Counter interface {
	Inc()
	Add(value float64)
}

type noop struct{}

func (c *noop) Inc()              {}
func (c *noop) Set(v float64)     {}
func (c *noop) Add(value float64) {}

// NoOpCounter is a Counter that does nothing
func NoOpCounter() Counter {
	return &noop{}
}

// Gauge is a standard metric gauge
type Gauge interface {
	Set(value float64)
}

// NoOpGauge is a Gauge that does nothing
func NoOpGauge() Gauge {
	return &noop{}
}

// Collector is an interface for creating metrics
type Collector interface {
	NewCounter(name string) Counter
	NewGuage(name string) Gauge
}
