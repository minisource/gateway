package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/url"
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
const healthCheckTimeout = 30 * time.Second

// healthFailThreshold is the number of consecutive failed probes required
// before a service is marked unhealthy. A single slow response (e.g. Next.js
// cold compile) must not take the service out of rotation.
const healthFailThreshold = 10

// tunnelDialTimeout bounds establishing the upstream TCP connection for
// hijacked streaming requests (SSE / WebSocket).
const tunnelDialTimeout = 10 * time.Second

// tunnelHandshakeTimeout bounds writing the request and reading the upstream
// response headers before the open-ended stream phase begins.
const tunnelHandshakeTimeout = 30 * time.Second

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
				MaxIdleConnDuration: 5 * time.Second, // upstreams (e.g. Next dev) drop idle conns fast — don't hold them longer than they do
				ReadTimeout:         timeout,
				WriteTimeout:        timeout,
				RetryIfErr: func(req *fasthttp.Request, _ int, _ error) (bool, bool) {
					// GATEWAY-002: a pooled keep-alive connection can go stale (the
					// upstream closed it after an idle gap) and fail on reuse — this
					// intermittently 502/500s parallel asset loads through the gateway.
					// Retrying once on a fresh connection fixes it. Only idempotent
					// methods are retried so a replayed request body can never
					// double-submit an upstream mutation.
					switch string(req.Header.Method()) {
					case http.MethodGet, http.MethodHead, http.MethodOptions:
						return false, true
					}
					return false, false
				},
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
		msg := respond.T(c, "errors.service_not_found", map[string]interface{}{"Service": serviceName})
		return respond.WriteError(c, fiber.StatusBadGateway, "SERVICE_NOT_FOUND", msg,
			fiber.Map{
				"success": false,
				"error": fiber.Map{
					"code":    "SERVICE_NOT_FOUND",
					"message": msg,
				},
			})
	}

	if !svc.Healthy {
		msg := respond.T(c, "errors.service_unavailable_name", map[string]interface{}{"Service": serviceName})
		return respond.WriteError(c, fiber.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", msg,
			fiber.Map{
				"success": false,
				"error": fiber.Map{
					"code":    "SERVICE_UNAVAILABLE",
					"message": msg,
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
	pathWithQuery := path
	if queryString != "" {
		pathWithQuery += "?" + queryString
	}

	targetURL := svc.URL + pathWithQuery

	// Create upstream request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	// Set full targetURL for fasthttp.Client.Do
	req.SetRequestURI(targetURL)
	req.Header.SetMethod(string(c.Request().Header.Method()))

	// Copy headers (skip hop-by-hop and websocket extensions that corrupt RSV1 bit)
	c.Request().Header.VisitAll(func(key, value []byte) {
		keyStr := string(key)
		if isHopByHopHeader(keyStr) || strings.EqualFold(keyStr, "Sec-WebSocket-Extensions") {
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

	// Streaming requests (SSE / WebSocket) bypass the pooled fasthttp client:
	// a transparent hijack tunnel relays raw bytes so chunks and upgrade frames
	// reach the client as they arrive instead of after the upstream connection
	// closes. forwardStream serializes req and releases it itself.
	if isStreamingRequest(c) {
		return p.forwardStream(c, req, svc)
	}

	defer fasthttp.ReleaseRequest(req)

	// Execute request
	if err := svc.Client.Do(req, resp); err != nil {
		msg := respond.T(c, "errors.upstream_error")
		return respond.WriteError(c, fiber.StatusBadGateway, "UPSTREAM_ERROR", msg,
			fiber.Map{
				"success": false,
				"error": fiber.Map{
					"code":    "UPSTREAM_ERROR",
					"message": msg,
					"details": err.Error(),
				},
			})
	}

	// Copy response headers (skip hop-by-hop and websocket extensions)
	resp.Header.VisitAll(func(key, value []byte) {
		keyStr := string(key)
		if isHopByHopHeader(keyStr) || strings.EqualFold(keyStr, "Sec-WebSocket-Extensions") {
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
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailers":            true,
		"Transfer-Encoding":   true,
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

// isStreamingRequest reports whether the client request needs a transparent
// byte-stream proxy instead of the buffered response path:
//   - WebSocket upgrade (Connection: Upgrade + Upgrade: websocket)
//   - Server-Sent Events (Accept: text/event-stream)
func isStreamingRequest(c *fiber.Ctx) bool {
	if strings.EqualFold(c.Get("Upgrade"), "websocket") {
		return strings.Contains(strings.ToLower(c.Get("Connection")), "upgrade")
	}
	return strings.Contains(strings.ToLower(c.Get("Accept")), "text/event-stream")
}

// forwardStream proxies a streaming request by hijacking the client
// connection and tunneling raw bytes to/from the upstream service. The
// pooled fasthttp client cannot represent an open-ended response body, so
// SSE chunks and WebSocket frames must flow over raw TCP.
func (p *ServiceProxy) forwardStream(c *fiber.Ctx, req *fasthttp.Request, svc *ServiceClient) error {
	// Set relative path on request URI for standard HTTP/1.1 raw TCP request line
	req.SetRequestURI(string(req.URI().RequestURI()))

	// WebSocket extension negotiation must reach the upstream untouched; the
	// shared request build skips it for the pooled-client path.
	if ext := c.Get("Sec-WebSocket-Extensions"); ext != "" {
		req.Header.Set("Sec-WebSocket-Extensions", ext)
	}

	// Serialize the upstream request now and release the pooled object: the
	// hijack handler runs after this handler returns and only consumes the
	// copied bytes, never req itself.
	var raw bytes.Buffer
	if _, err := req.WriteTo(&raw); err != nil {
		fasthttp.ReleaseRequest(req)
		msg := respond.T(c, "errors.upstream_error")
		return respond.WriteError(c, fiber.StatusBadGateway, "UPSTREAM_ERROR", msg,
			fiber.Map{
				"success": false,
				"error": fiber.Map{
					"code":    "UPSTREAM_ERROR",
					"message": msg,
					"details": err.Error(),
				},
			})
	}
	fasthttp.ReleaseRequest(req)

	// Tell fasthttp to write no response itself: the tunnel writes the status
	// line, headers and body directly to the hijacked connection.
	c.Context().HijackSetNoResponse(true)
	c.Context().Hijack(func(clientConn net.Conn) {
		p.runTunnel(clientConn, raw.Bytes(), svc)
	})
	return nil
}

// runTunnel relays a hijacked client connection to the upstream service:
// dial → write request → relay upstream headers → bidirectional byte copy.
// The handshake phase is bounded; the stream phase has no deadline because
// SSE heartbeats and WebSocket sessions are open-ended by nature.
func (p *ServiceProxy) runTunnel(clientConn net.Conn, raw []byte, svc *ServiceClient) {
	defer clientConn.Close()

	upstreamAddr, err := upstreamHostPort(svc.URL)
	if err != nil {
		return
	}

	upConn, err := net.DialTimeout("tcp", upstreamAddr, tunnelDialTimeout)
	if err != nil {
		return
	}
	defer upConn.Close()

	deadline := time.Now().Add(tunnelHandshakeTimeout)
	_ = upConn.SetDeadline(deadline)
	_ = clientConn.SetDeadline(deadline)

	if _, err := upConn.Write(raw); err != nil {
		return
	}

	// Read the raw upstream HTTP response head (until \r\n\r\n) and forward directly to client.
	br := bufio.NewReader(upConn)
	var responseHead bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		responseHead.WriteString(line)
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	if _, err := clientConn.Write(responseHead.Bytes()); err != nil {
		return
	}

	// Stream phase: long-lived connection, no deadlines.
	_ = upConn.SetDeadline(time.Time{})
	_ = clientConn.SetDeadline(time.Time{})

	done := make(chan struct{}, 2)
	// Upstream → client. br preserves any body bytes already buffered.
	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(clientConn, br)
	}()
	// Client → upstream (WebSocket frames, late request bodies).
	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(upConn, clientConn)
	}()

	// When one direction ends (upstream closed an SSE stream, or either side
	// closed a WebSocket session), close both ends to unblock the other copy.
	<-done
	_ = upConn.Close()
	_ = clientConn.Close()
	<-done
}

// upstreamHostPort extracts the dialable host:port from a service base URL,
// defaulting to port 80/443 when the scheme implies it and no port is given.
func upstreamHostPort(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	host := u.Host
	if host == "" {
		host = strings.TrimPrefix(baseURL, "http://")
		host = strings.TrimPrefix(host, "https://")
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		switch u.Scheme {
		case "https":
			return net.JoinHostPort(host, "443"), nil
		default:
			return net.JoinHostPort(host, "80"), nil
		}
	}
	return host, nil
}
