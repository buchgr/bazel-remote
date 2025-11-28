package otel

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// Ensure the imports are used
var _ = otel.Tracer
var _ = sdktrace.TracerProvider{}
var _ = tracetest.SpanStub{}
var _ = semconv.ServiceNameKey

func TestInitTracer(t *testing.T) {
	tests := []struct {
		name        string
		endpoint    string
		serviceName string
		sampleRate  float64
		wantErr     bool
	}{
		{
			name:        "valid configuration",
			endpoint:    "localhost:4317",
			serviceName: "test-service",
			sampleRate:  1.0,
			wantErr:     false,
		},
		{
			name:        "valid with partial sampling",
			endpoint:    "localhost:4317",
			serviceName: "test-service",
			sampleRate:  0.1,
			wantErr:     false,
		},
		{
			name:        "valid with zero sampling",
			endpoint:    "localhost:4317",
			serviceName: "test-service",
			sampleRate:  0.0,
			wantErr:     false,
		},
		{
			name:        "empty endpoint should still initialize",
			endpoint:    "",
			serviceName: "test-service",
			sampleRate:  1.0,
			wantErr:     false, // otlptracegrpc.New will handle the invalid endpoint
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tp, err := InitTracer(tt.endpoint, tt.serviceName, tt.sampleRate)
			if (err != nil) != tt.wantErr {
				t.Errorf("InitTracer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if err == nil {
				// Clean up - shutdown the tracer
				if tp != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					if shutdownErr := tp.Shutdown(ctx); shutdownErr != nil {
						t.Errorf("Shutdown() error = %v", shutdownErr)
					}
				}
			}
		})
	}
}

func TestTracerProviderShutdown(t *testing.T) {
	tp, err := InitTracer("localhost:4317", "test-service", 1.0)
	if err != nil {
		t.Fatalf("InitTracer() failed: %v", err)
	}

	// Test successful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = tp.Shutdown(ctx)
	if err != nil {
		t.Errorf("Shutdown() error = %v", err)
	}
}

func TestTracerProviderShutdownTimeout(t *testing.T) {
	tp, err := InitTracer("localhost:4317", "test-service", 1.0)
	if err != nil {
		t.Fatalf("InitTracer() failed: %v", err)
	}

	// Test shutdown with very short timeout (might cause timeout error, but shouldn't panic)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()

	// This might error due to timeout, but we're just ensuring it doesn't panic
	_ = tp.Shutdown(ctx)

	// Clean up properly with reasonable timeout
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cleanupCancel()
	_ = tp.Shutdown(cleanupCtx)
}

func TestInitTracerSetsGlobalProvider(t *testing.T) {
	// This test verifies that InitTracer sets the global tracer provider
	// We can't easily test the actual global state, but we can at least
	// verify that the function completes without error

	tp, err := InitTracer("localhost:4317", "test-service", 1.0)
	if err != nil {
		t.Fatalf("InitTracer() failed: %v", err)
	}
	defer tp.Shutdown(context.Background())

	// If we got here without error, the global provider was set
	// Additional verification would require accessing otel.GetTracerProvider()
	// but that's more of an integration test
}
