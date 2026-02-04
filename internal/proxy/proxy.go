package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/config"
	"github.com/valyala/fasthttp"
)

// ServiceProxy handles proxying requests to backend services
type ServiceProxy struct {
	services map[string]*ServiceClient
	mu       sync.RWMutex
}

// ServiceClient represents a connection to a backend service
type ServiceClient struct {
	Name       string
	URL        string
	Client     *fasthttp.Client
	HealthPath string
	Healthy    bool
	LastCheck  time.Time
}

// NewServiceProxy creates a new service proxy
func NewServiceProxy(cfg *config.ServicesConfig) *ServiceProxy {
	proxy := &ServiceProxy{
		services: make(map[string]*ServiceClient),
	}

	// Initialize auth service
	proxy.services["auth"] = &ServiceClient{
		Name:       "auth",
		URL:        cfg.Auth.URL,
		HealthPath: cfg.Auth.HealthPath,
		Healthy:    true,
		Client: &fasthttp.Client{
			MaxConnsPerHost:     cfg.Auth.MaxConnsPerHost,
			MaxIdleConnDuration: 30 * time.Second,
			ReadTimeout:         cfg.Auth.Timeout,
			WriteTimeout:        cfg.Auth.Timeout,
		},
	}

	// Initialize notifier service
	proxy.services["notifier"] = &ServiceClient{
		Name:       "notifier",
		URL:        cfg.Notifier.URL,
		HealthPath: cfg.Notifier.HealthPath,
		Healthy:    true,
		Client: &fasthttp.Client{
			MaxConnsPerHost:     cfg.Notifier.MaxConnsPerHost,
			MaxIdleConnDuration: 30 * time.Second,
			ReadTimeout:         cfg.Notifier.Timeout,
			WriteTimeout:        cfg.Notifier.Timeout,
		},
	}

	return proxy
}

// GetService returns a service client by name
func (p *ServiceProxy) GetService(name string) (*ServiceClient, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	svc, ok := p.services[name]
	return svc, ok
}

// Forward proxies a request to the target service
func (p *ServiceProxy) Forward(c *fiber.Ctx, serviceName string, stripPrefix string) error {
	svc, ok := p.GetService(serviceName)
	if !ok {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error": fmt.Sprintf("service %s not found", serviceName),
		})
	}

	if !svc.Healthy {
		return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
			"error": fmt.Sprintf("service %s is unavailable", serviceName),
		})
	}

	// Build target URL
	path := string(c.Request().URI().Path())
	if stripPrefix != "" {
		path = strings.TrimPrefix(path, stripPrefix)
		if path == "" {
			path = "/"
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

	// Copy headers
	c.Request().Header.VisitAll(func(key, value []byte) {
		keyStr := string(key)
		// Skip hop-by-hop headers
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
	req.Header.Set("X-Request-ID", c.GetRespHeader("X-Request-ID"))

	// Copy body
	if len(c.Body()) > 0 {
		req.SetBody(c.Body())
	}

	// Execute request
	if err := svc.Client.Do(req, resp); err != nil {
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{
			"error":   "upstream request failed",
			"details": err.Error(),
		})
	}

	// Copy response headers
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

// HealthCheck checks the health of a service
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

	if err := svc.Client.DoTimeout(req, resp, 5*time.Second); err != nil {
		p.setServiceHealth(serviceName, false)
		return false
	}

	healthy := resp.StatusCode() >= 200 && resp.StatusCode() < 300
	p.setServiceHealth(serviceName, healthy)
	return healthy
}

// setServiceHealth updates service health status
func (p *ServiceProxy) setServiceHealth(name string, healthy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if svc, ok := p.services[name]; ok {
		svc.Healthy = healthy
		svc.LastCheck = time.Now()
	}
}

// StartHealthChecks starts background health checking
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

// GetServicesHealth returns health status of all services
func (p *ServiceProxy) GetServicesHealth() map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	health := make(map[string]bool)
	for name, svc := range p.services {
		health[name] = svc.Healthy
	}
	return health
}

// isHopByHopHeader checks if header should not be forwarded
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

// Close cleans up the proxy resources
func (p *ServiceProxy) Close() error {
	// fasthttp.Client doesn't need explicit cleanup
	return nil
}
