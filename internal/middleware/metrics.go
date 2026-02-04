package middleware

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Request metrics
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_http_requests_total",
			Help: "Total number of HTTP requests",
		},
		[]string{"method", "path", "service", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		},
		[]string{"method", "path", "service"},
	)

	httpRequestSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_http_request_size_bytes",
			Help:    "HTTP request size in bytes",
			Buckets: prometheus.ExponentialBuckets(100, 10, 7),
		},
		[]string{"method", "path"},
	)

	httpResponseSize = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_http_response_size_bytes",
			Help:    "HTTP response size in bytes",
			Buckets: prometheus.ExponentialBuckets(100, 10, 7),
		},
		[]string{"method", "path"},
	)

	// Active connections
	activeConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "gateway_active_connections",
			Help: "Number of active connections",
		},
	)

	// Circuit breaker metrics
	circuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gateway_circuit_breaker_state",
			Help: "Circuit breaker state (0=closed, 1=half-open, 2=open)",
		},
		[]string{"service"},
	)

	// Rate limiter metrics
	rateLimitExceeded = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_rate_limit_exceeded_total",
			Help: "Total number of rate limit exceeded responses",
		},
		[]string{"path"},
	)

	// Upstream metrics
	upstreamErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gateway_upstream_errors_total",
			Help: "Total number of upstream errors",
		},
		[]string{"service", "error_type"},
	)

	upstreamLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gateway_upstream_latency_seconds",
			Help:    "Upstream service latency in seconds",
			Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		},
		[]string{"service"},
	)
)

// Metrics returns Prometheus metrics middleware
func Metrics() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		activeConnections.Inc()

		// Get service name (set by router)
		serviceName := "gateway"
		if svc, ok := c.Locals("service").(string); ok && svc != "" {
			serviceName = svc
		}

		// Process request
		err := c.Next()

		// Record metrics
		duration := time.Since(start).Seconds()
		status := strconv.Itoa(c.Response().StatusCode())
		method := c.Method()
		path := c.Route().Path

		// Normalize path for metrics (avoid high cardinality)
		if path == "" {
			path = c.Path()
		}

		httpRequestsTotal.WithLabelValues(method, path, serviceName, status).Inc()
		httpRequestDuration.WithLabelValues(method, path, serviceName).Observe(duration)
		httpRequestSize.WithLabelValues(method, path).Observe(float64(len(c.Body())))
		httpResponseSize.WithLabelValues(method, path).Observe(float64(len(c.Response().Body())))

		activeConnections.Dec()

		// Record rate limit exceeded
		if c.Response().StatusCode() == fiber.StatusTooManyRequests {
			rateLimitExceeded.WithLabelValues(path).Inc()
		}

		// Record upstream errors
		if c.Response().StatusCode() >= 500 {
			upstreamErrors.WithLabelValues(serviceName, "5xx").Inc()
		} else if c.Response().StatusCode() == 502 || c.Response().StatusCode() == 503 {
			upstreamErrors.WithLabelValues(serviceName, "upstream_unavailable").Inc()
		}

		upstreamLatency.WithLabelValues(serviceName).Observe(duration)

		return err
	}
}

// UpdateCircuitBreakerMetric updates circuit breaker state metric
func UpdateCircuitBreakerMetric(service string, state int) {
	circuitBreakerState.WithLabelValues(service).Set(float64(state))
}

// GetMetricsHandler returns handler for /metrics endpoint
func GetMetricsHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Prometheus HTTP handler integration
		// This is handled by promhttp in main.go
		return c.Next()
	}
}
