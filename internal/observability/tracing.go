package observability

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"math"
	"os"
	"strconv"
)

func Setup(ctx context.Context) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	sample := 0.01
	if raw := os.Getenv("MQTITAN_TRACE_SAMPLE_RATIO"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			return nil, errors.New("trace sample ratio must be between zero and one")
		}
		sample = v
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(1024)), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sample))), sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", "mqtitan"))))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
