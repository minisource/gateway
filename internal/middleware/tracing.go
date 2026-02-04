package middleware

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

// TracerProvider holds the tracer provider
var tracerProvider *sdktrace.TracerProvider

// InitTracer initializes OpenTelemetry tracer
func InitTracer(cfg config.TracingConfig) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(ctx context.Context) error { return nil }, nil
	}

	ctx := context.Background()

	// Create OTLP exporter
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(cfg.Endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	// Create resource
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion("1.0.0"),
		),
	)
	if err != nil {
		return nil, err
	}

	// Create tracer provider
	tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)),
	)

	// Set global tracer provider
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tracerProvider.Shutdown, nil
}

// Tracing returns distributed tracing middleware
func Tracing(serviceName string) fiber.Handler {
	tracer := otel.Tracer(serviceName)

	return func(c *fiber.Ctx) error {
		// Extract trace context from incoming request
		ctx := otel.GetTextMapPropagator().Extract(
			c.UserContext(),
			&fiberHeaderCarrier{c},
		)

		// Start span
		spanName := c.Method() + " " + c.Path()
		ctx, span := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()

		// Set span attributes
		span.SetAttributes(
			semconv.HTTPMethod(c.Method()),
			semconv.HTTPRoute(c.Route().Path),
			semconv.HTTPURL(c.OriginalURL()),
			semconv.HTTPUserAgent(c.Get("User-Agent")),
			semconv.HTTPClientIP(c.IP()),
		)

		// Add custom attributes
		if requestID, ok := c.Locals("request_id").(string); ok {
			span.SetAttributes(attribute.String("request.id", requestID))
		}
		if tenantID, ok := c.Locals("tenant_id").(string); ok {
			span.SetAttributes(attribute.String("tenant.id", tenantID))
		}
		if userID, ok := c.Locals("user_id").(string); ok {
			span.SetAttributes(attribute.String("user.id", userID))
		}
		if service, ok := c.Locals("service").(string); ok {
			span.SetAttributes(attribute.String("upstream.service", service))
		}

		// Store context and span in Fiber context
		c.SetUserContext(ctx)
		c.Locals("span", span)

		// Inject trace context for downstream propagation
		otel.GetTextMapPropagator().Inject(ctx, &fiberRequestCarrier{c})

		// Process request
		start := time.Now()
		err := c.Next()
		duration := time.Since(start)

		// Set response attributes
		statusCode := c.Response().StatusCode()
		span.SetAttributes(
			semconv.HTTPStatusCode(statusCode),
			attribute.Int64("http.response_content_length", int64(len(c.Response().Body()))),
			attribute.Float64("http.duration_ms", float64(duration.Milliseconds())),
		)

		// Set span status based on HTTP status code
		if statusCode >= 400 {
			span.SetStatus(codes.Error, "HTTP error")
		} else {
			span.SetStatus(codes.Ok, "")
		}

		// Record error if any
		if err != nil {
			span.RecordError(err)
		}

		return err
	}
}

// fiberHeaderCarrier adapts Fiber context for trace extraction
type fiberHeaderCarrier struct {
	c *fiber.Ctx
}

func (c *fiberHeaderCarrier) Get(key string) string {
	return c.c.Get(key)
}

func (c *fiberHeaderCarrier) Set(key, value string) {
	c.c.Set(key, value)
}

func (c *fiberHeaderCarrier) Keys() []string {
	var keys []string
	c.c.Request().Header.VisitAll(func(key, _ []byte) {
		keys = append(keys, string(key))
	})
	return keys
}

// fiberRequestCarrier adapts Fiber request for trace injection
type fiberRequestCarrier struct {
	c *fiber.Ctx
}

func (c *fiberRequestCarrier) Get(key string) string {
	return string(c.c.Request().Header.Peek(key))
}

func (c *fiberRequestCarrier) Set(key, value string) {
	c.c.Request().Header.Set(key, value)
}

func (c *fiberRequestCarrier) Keys() []string {
	var keys []string
	c.c.Request().Header.VisitAll(func(key, _ []byte) {
		keys = append(keys, string(key))
	})
	return keys
}

// CreateSpan creates a child span for operations
func CreateSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	tracer := otel.Tracer("gateway")
	return tracer.Start(ctx, name, opts...)
}

// GetSpanFromContext extracts span from Fiber context
func GetSpanFromContext(c *fiber.Ctx) trace.Span {
	if span, ok := c.Locals("span").(trace.Span); ok {
		return span
	}
	return nil
}
