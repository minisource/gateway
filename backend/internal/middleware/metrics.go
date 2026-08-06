package middleware

import (
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus"
)

// gatewayMetrics holds all Prometheus metric collectors for the gateway.
// Created once via init function so air hot-reload is safe.
type gatewayMetrics struct {
	httpRequestsTotal   *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
	httpRequestSize     *prometheus.HistogramVec
	httpResponseSize    *prometheus.HistogramVec
	activeConnections   prometheus.Gauge
	circuitBreakerState *prometheus.GaugeVec
	rateLimitExceeded   *prometheus.CounterVec
	upstreamErrors      *prometheus.CounterVec
	upstreamLatency     *prometheus.HistogramVec
}

var (
	metrics     *gatewayMetrics
	metricsOnce sync.Once
)

func initMetrics() {
	// Use a custom registry scoped to the gateway so there is no conflict
	// with metrics registered by go-common or other packages.
	reg := prometheus.NewRegistry()

	m := &gatewayMetrics{
		httpRequestsTotal: promautoNewCounterVec(reg, prometheus.CounterOpts{
			Name: "gateway_http_requests_total",
			Help: "Total number of HTTP requests",
		}, []string{"method", "path", "service", "status"}),

		httpRequestDuration: promautoNewHistogramVec(reg, prometheus.HistogramOpts{
			Name:    "gateway_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"method", "path", "service"}),

		httpRequestSize: promautoNewHistogramVec(reg, prometheus.HistogramOpts{
			Name:    "gateway_http_request_size_bytes",
			Help:    "HTTP request size in bytes",
			Buckets: prometheus.ExponentialBuckets(100, 10, 7),
		}, []string{"method", "path"}),

		httpResponseSize: promautoNewHistogramVec(reg, prometheus.HistogramOpts{
			Name:    "gateway_http_response_size_bytes",
			Help:    "HTTP response size in bytes",
			Buckets: prometheus.ExponentialBuckets(100, 10, 7),
		}, []string{"method", "path"}),

		activeConnections: promautoNewGauge(reg, prometheus.GaugeOpts{
			Name: "gateway_active_connections",
			Help: "Number of active connections",
		}),

		circuitBreakerState: promautoNewGaugeVec(reg, prometheus.GaugeOpts{
			Name: "gateway_circuit_breaker_state",
			Help: "Circuit breaker state (0=closed, 1=half-open, 2=open)",
		}, []string{"service"}),

		rateLimitExceeded: promautoNewCounterVec(reg, prometheus.CounterOpts{
			Name: "gateway_rate_limit_exceeded_total",
			Help: "Total number of rate limit exceeded responses",
		}, []string{"path"}),

		upstreamErrors: promautoNewCounterVec(reg, prometheus.CounterOpts{
			Name: "gateway_upstream_errors_total",
			Help: "Total number of upstream errors",
		}, []string{"service", "error_type"}),

		upstreamLatency: promautoNewHistogramVec(reg, prometheus.HistogramOpts{
			Name:    "gateway_upstream_latency_seconds",
			Help:    "Upstream service latency in seconds",
			Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"service"}),
	}

	// Register Go runtime and process collectors with the custom registry
	// so /metrics shows everything expected.
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	metrics = m
	metricsRegistry = reg
}

// promautoNewCounterVec is like promauto.With(reg).NewCounterVec but safe
// against duplicate registration on hot-reload.
func promautoNewCounterVec(reg *prometheus.Registry, opts prometheus.CounterOpts, labels []string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(opts, labels)
	err := reg.Register(c)
	if err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(*prometheus.CounterVec)
		}
		panic(err)
	}
	return c
}

func promautoNewHistogramVec(reg *prometheus.Registry, opts prometheus.HistogramOpts, labels []string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(opts, labels)
	err := reg.Register(h)
	if err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(*prometheus.HistogramVec)
		}
		panic(err)
	}
	return h
}

func promautoNewGauge(reg *prometheus.Registry, opts prometheus.GaugeOpts) prometheus.Gauge {
	g := prometheus.NewGauge(opts)
	err := reg.Register(g)
	if err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(prometheus.Gauge)
		}
		panic(err)
	}
	return g
}

func promautoNewGaugeVec(reg *prometheus.Registry, opts prometheus.GaugeOpts, labels []string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(opts, labels)
	err := reg.Register(g)
	if err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(*prometheus.GaugeVec)
		}
		panic(err)
	}
	return g
}

// Metrics returns Prometheus metrics middleware
func Metrics() fiber.Handler {
	metricsOnce.Do(initMetrics)

	return func(c *fiber.Ctx) error {
		start := time.Now()
		activeConnections := metrics.activeConnections
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

		// Normalize path for metrics (avoid high cardinality and metric collision)
		reqPath := c.Path()
		if route := c.Route(); route != nil && route.Path != "" && route.Path != "/*" {
			reqPath = route.Path
		}

		metrics.httpRequestsTotal.WithLabelValues(method, reqPath, serviceName, status).Inc()
		metrics.httpRequestDuration.WithLabelValues(method, reqPath, serviceName).Observe(duration)
		metrics.httpRequestSize.WithLabelValues(method, reqPath).Observe(float64(len(c.Body())))
		metrics.httpResponseSize.WithLabelValues(method, reqPath).Observe(float64(len(c.Response().Body())))

		activeConnections.Dec()

		// Record rate limit exceeded
		if c.Response().StatusCode() == fiber.StatusTooManyRequests {
			metrics.rateLimitExceeded.WithLabelValues(reqPath).Inc()
		}

		// Record upstream errors
		if c.Response().StatusCode() >= 500 {
			metrics.upstreamErrors.WithLabelValues(serviceName, "5xx").Inc()
		} else if c.Response().StatusCode() == 502 || c.Response().StatusCode() == 503 {
			metrics.upstreamErrors.WithLabelValues(serviceName, "upstream_unavailable").Inc()
		}

		metrics.upstreamLatency.WithLabelValues(serviceName).Observe(duration)

		return err
	}
}

// UpdateCircuitBreakerMetric updates circuit breaker state metric
func UpdateCircuitBreakerMetric(service string, state int) {
	metricsOnce.Do(initMetrics)
	metrics.circuitBreakerState.WithLabelValues(service).Set(float64(state))
}

// GetMetricsRegistry returns the custom registry for use with promhttp.
func GetMetricsRegistry() *prometheus.Registry {
	metricsOnce.Do(initMetrics)
	// We need to extract the registry from the first registered collector.
	// Instead, we expose a simple helper: Register a no-op to get the registry.
	// Actually, we stored the registry in the initMetrics closure.
	// For promhttp.Handler we need to use prometheus.Gatherer.
	// Since we use a custom registry, we need to return it.
	// The simplest approach: store the registry globally.
	return metricsRegistry
}

var metricsRegistry *prometheus.Registry

func init() {
	metricsOnce.Do(initMetrics)
}
