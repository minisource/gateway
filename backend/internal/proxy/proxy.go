package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/config"
	"github.com/minisource/gateway/internal/respond"
	"github.com/valyala/fasthttp"
	"go.opentelemetry.io/otel"
)

// ServiceProxy handles proxying requests to backend services.
type ServiceProxy struct {
	services map[string]*ServiceClient
	mu       sync.RWMutex
}

// ServiceClient represents a connection to a backend service.
type ServiceClient struct {
	Name           string
	URL            string
	Client         *fasthttp.Client
	HealthPath     string
	Healthy        bool
	HealthFailures int
	LastCheck      time.Time
}

// healthCheckTimeout bounds each individual health probe. Next.js dev servers
// can take longer than a typical liveness check during cold/re-compilation, so
// this must be generous enough to avoid false negatives in local development.
const healthCheckTimeout = 15 * time.Second

// healthFailThreshold is the number of consecutive failed probes required
// before a service is marked unhealthy. A single slow response (e.g. Next.js
// cold compile) must not take the service out of rotation.
const healthFailThreshold = 2

// NewServiceProxy creates a proxy from the GatewayConfig services map.
func NewServiceProxy(cfg *config.GatewayConfig) *ServiceProxy {
	proxy := &ServiceProxy{
		services: make(map[string]*ServiceClient),
	}
	for name, svc := range cfg.Services {
		if svc == nil || svc.BaseURL == "" {
			continue
		}
		timeout := svc.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		proxy.services[name] = &ServiceClient{
			Name:       name,
			URL:        strings.TrimRight(svc.BaseURL, "/"),
			HealthPath: svc.HealthPath,
			Healthy:    true,
			Client: &fasthttp.Client{
				MaxConnsPerHost:     200,
				MaxIdleConnDuration: 30 * time.Second,
				ReadTimeout:         timeout,
				WriteTimeout:        timeout,
			},
		}
	}
	return proxy
}

// GetService returns a service client by name.
func (p *ServiceProxy) GetService(name string) (*ServiceClient, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	svc, ok := p.services[name]
	return svc, ok
}

// Forward proxies a request to the target service (backward compatible).
func (p *ServiceProxy) Forward(c *fiber.Ctx, serviceName string, stripPrefix string) error {
	return p.ForwardWithPrefix(c, serviceName, stripPrefix, "")
}

// ForwardWithPrefix proxies a request with stripPrefix and optional upstreamPathPrefix.
// stripPrefix: path prefix to remove from the request path before forwarding
// upstreamPathPrefix: path prefix to prepend to the upstream URL
func (p *ServiceProxy) ForwardWithPrefix(c *fiber.Ctx, serviceName string, stripPrefix string, upstreamPathPrefix string) error {
	svc, ok := p.GetService(serviceName)
	if !ok {
		return respond.WriteError(c, fiber.StatusBadGateway, "SERVICE_NOT_FOUND",
			fmt.Sprintf("service %s not found", serviceName),
			fiber.Map{
				"success": false,
				"error": fiber.Map{
					"code":    "SERVICE_NOT_FOUND",
					"message": fmt.Sprintf("service %s not found", serviceName),
				},
			})
	}

	if !svc.Healthy {
		return respond.WriteError(c, fiber.StatusServiceUnavailable, "SERVICE_UNAVAILABLE",
			fmt.Sprintf("service %s is unavailable", serviceName),
			fiber.Map{
				"success": false,
				"error": fiber.Map{
					"code":    "SERVICE_UNAVAILABLE",
					"message": fmt.Sprintf("service %s is unavailable", serviceName),
				},
			})
	}

	// Build target URL
	path := string(c.Request().URI().Path())

	// Strip prefix from path
	if stripPrefix != "" {
		path = strings.TrimPrefix(path, stripPrefix)
		if path == "" {
			path = "/"
		}
	}

	// Prepend upstream path prefix.
	// When path is "/" (fully stripped), use the prefix directly
	// to avoid a trailing slash that could break exact route matching.
	if upstreamPathPrefix != "" {
		if path == "/" {
			path = strings.TrimRight(upstreamPathPrefix, "/")
		} else {
			path = strings.TrimRight(upstreamPathPrefix, "/") + path
		}
	}

	queryString := string(c.Request().URI().QueryString())
	targetURL := svc.URL + path
	if queryString != "" {
		targetURL += "?" + queryString
	}

	// Create upstream request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Copy request
	req.SetRequestURI(targetURL)
	req.Header.SetMethod(string(c.Request().Header.Method()))

	// Copy headers (skip hop-by-hop)
	c.Request().Header.VisitAll(func(key, value []byte) {
		keyStr := string(key)
		if isHopByHopHeader(keyStr) {
			return
		}
		req.Header.SetBytesKV(key, value)
	})

	// Set forwarding headers
	req.Header.Set("X-Forwarded-For", c.IP())
	req.Header.Set("X-Forwarded-Host", string(c.Request().Host()))
	req.Header.Set("X-Forwarded-Proto", c.Protocol())
	req.Header.Set("X-Real-IP", c.IP())

	// Propagate request ID (set on the request header by the shared RequestID
	// middleware; falls back to the client-supplied header).
	if rid := c.Get("X-Request-ID"); rid != "" {
		req.Header.Set("X-Request-ID", rid)
	}

	// Copy body
	if len(c.Body()) > 0 {
		req.SetBody(c.Body())
	}

	// Inject trace context propagation for downstream
	otel.GetTextMapPropagator().Inject(c.UserContext(), &fasthttpHeaderCarrier{h: &req.Header})

	// Execute request
	if err := svc.Client.Do(req, resp); err != nil {
		return respond.WriteError(c, fiber.StatusBadGateway, "UPSTREAM_ERROR",
			"Upstream request failed",
			fiber.Map{
				"success": false,
				"error": fiber.Map{
					"code":    "UPSTREAM_ERROR",
					"message": "Upstream request failed",
					"details": err.Error(),
				},
			})
	}

	// Copy response headers (skip hop-by-hop)
	resp.Header.VisitAll(func(key, value []byte) {
		keyStr := string(key)
		if isHopByHopHeader(keyStr) {
			return
		}
		c.Set(keyStr, string(value))
	})

	// Copy response
	c.Status(resp.StatusCode())
	return c.Send(resp.Body())
}

// StartHealthChecks starts background health checking.
func (p *ServiceProxy) StartHealthChecks(interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			for name := range p.services {
				p.HealthCheck(name)
			}
		}
	}()
}

// HealthCheck checks the health of a service. A service is marked unhealthy
// only after healthFailThreshold consecutive failures; any success resets the
// failure counter and restores it to healthy.
func (p *ServiceProxy) HealthCheck(serviceName string) bool {
	svc, ok := p.GetService(serviceName)
	if !ok {
		return false
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(svc.URL + svc.HealthPath)
	req.Header.SetMethod("GET")

	var healthy bool
	if err := svc.Client.DoTimeout(req, resp, healthCheckTimeout); err == nil {
		healthy = resp.StatusCode() >= 200 && resp.StatusCode() < 300
	}

	p.recordHealth(serviceName, healthy)
	return healthy
}

// recordHealth updates the consecutive-failure counter and health state.
func (p *ServiceProxy) recordHealth(name string, healthy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	svc, ok := p.services[name]
	if !ok {
		return
	}
	svc.LastCheck = time.Now()
	if healthy {
		svc.HealthFailures = 0
		svc.Healthy = true
		return
	}
	svc.HealthFailures++
	if svc.HealthFailures >= healthFailThreshold {
		svc.Healthy = false
	}
}

// GetServicesHealth returns health status of all services.
func (p *ServiceProxy) GetServicesHealth() map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	health := make(map[string]bool, len(p.services))
	for name, svc := range p.services {
		health[name] = svc.Healthy
	}
	return health
}

// Close cleans up the proxy resources.
func (p *ServiceProxy) Close() error {
	return nil
}

func isHopByHopHeader(header string) bool {
	hopByHopHeaders := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailers":            true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
	}
	return hopByHopHeaders[http.CanonicalHeaderKey(header)]
}

type fasthttpHeaderCarrier struct {
	h *fasthttp.RequestHeader
}

func (c *fasthttpHeaderCarrier) Get(key string) string {
	return string(c.h.Peek(key))
}

func (c *fasthttpHeaderCarrier) Set(key, value string) {
	c.h.Set(key, value)
}

func (c *fasthttpHeaderCarrier) Keys() []string {
	var keys []string
	c.h.VisitAll(func(k, v []byte) {
		keys = append(keys, string(k))
	})
	return keys
}
