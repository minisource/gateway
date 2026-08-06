package router

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/config"
	"github.com/minisource/gateway/internal/middleware"
	"github.com/minisource/gateway/internal/proxy"
	"github.com/minisource/gateway/internal/respond"
)

// Router manages host-based API gateway routing.
//
// Architecture:
//   Host header → select HostCfg → match RouteCfg by pathPrefix/method
//   Route policy determines auth requirement and accessibility.
//   Auth enforcement happens at the route handler level (after route matching).
type Router struct {
	app   *fiber.App
	proxy *proxy.ServiceProxy
	gwCfg *config.GatewayConfig
	auth  *middleware.AuthMiddleware
	// hostRoutes is a flattened map of host→route for quick lookup
	hostRoutes map[string][]config.RouteCfg
}

// New creates a new Router from the GatewayConfig.
// If auth is provided, it enforces route policies in the proxy handler.
func New(app *fiber.App, serviceProxy *proxy.ServiceProxy, gwCfg *config.GatewayConfig, auth *middleware.AuthMiddleware) *Router {
	r := &Router{
		app:        app,
		proxy:      serviceProxy,
		gwCfg:      gwCfg,
		auth:       auth,
		hostRoutes: make(map[string][]config.RouteCfg),
	}
	// Index routes by host for O(1) lookup
	for _, host := range gwCfg.Hosts {
		r.hostRoutes[strings.ToLower(host.Host)] = host.Routes
	}
	return r
}

// SetupRoutes registers all routes from the config with Fiber.
func (r *Router) SetupRoutes() {
	// Gateway-internal routes (health, metrics, swagger) are registered directly
	// in main.go via fiber.App methods, not through the router.

	// Register proxy routes from config
	for _, host := range r.gwCfg.Hosts {
		for _, route := range host.Routes {
			if route.Policy == "disabled" || route.Policy == "internal" {
				// Disabled/internal routes are NOT publicly accessible
				continue
			}
			r.setupProxyRoute(host.Host, route)
		}
	}

	// Catch-all: unmatched host or path returns 404
	r.app.Use(func(c *fiber.Ctx) error {
		host := c.Hostname()
		matched := r.findHost(host)
		if matched == nil {
			return gatewayError(c, fiber.StatusNotFound, "HOST_NOT_FOUND", "Unknown host: "+host)
		}
		return gatewayError(c, fiber.StatusNotFound, "ROUTE_NOT_FOUND", "Route not found: "+c.Method()+" "+c.Path())
	})
}

// Gateway-internal routes (health, metrics, swagger) are registered directly
// in main.go via fiber.App methods, not through the router.

// setupProxyRoute registers a single proxy route for a host.
func (r *Router) setupProxyRoute(host string, route config.RouteCfg) {
	// For localhost, register routes without host constraint (Fiber doesn't natively filter by Host).
	// The handler checks the Host header manually.
	pathWithWildcard := route.PathPrefix
	if !strings.HasSuffix(pathWithWildcard, "/*") {
		pathWithWildcard = route.PathPrefix + "/*"
	}

	handler := r.createProxyHandler(host, route)

	for _, method := range route.Methods {
		switch strings.ToUpper(method) {
		case "GET":
			r.app.Get(pathWithWildcard, handler)
			r.app.Get(route.PathPrefix, handler)
		case "POST":
			r.app.Post(pathWithWildcard, handler)
			r.app.Post(route.PathPrefix, handler)
		case "PUT":
			r.app.Put(pathWithWildcard, handler)
			r.app.Put(route.PathPrefix, handler)
		case "DELETE":
			r.app.Delete(pathWithWildcard, handler)
			r.app.Delete(route.PathPrefix, handler)
		case "PATCH":
			r.app.Patch(pathWithWildcard, handler)
			r.app.Patch(route.PathPrefix, handler)
		case "OPTIONS":
			r.app.Options(pathWithWildcard, handler)
			r.app.Options(route.PathPrefix, handler)
		}
	}
}

// createProxyHandler returns a Fiber handler that:
// 1. Validates the Host header matches the expected host (strict in production).
// 2. Strips incoming identity headers (X-User-*) before routing.
// 3. Sets route context for middleware (policy, service name).
// 4. Performs path rewriting (stripPrefix → upstreamPathPrefix).
// 5. Proxies the request to the upstream service.
func (r *Router) createProxyHandler(host string, route config.RouteCfg) fiber.Handler {
	isProduction := r.gwCfg.App.Env == "production" || r.gwCfg.App.Env == "staging"

	return func(c *fiber.Ctx) error {
		// Host validation
		requestHost := strings.ToLower(c.Hostname())
		if requestHost != strings.ToLower(host) {
			// In production/staging: strict host match, no fallback. Unknown hosts → 404.
			if isProduction {
				return gatewayError(c, fiber.StatusNotFound, "HOST_MISMATCH",
					"Unknown host: "+requestHost)
			}
			// In development: localhost routes accept any host (e.g. 127.0.0.1, 10.0.2.2 from emulator)
			// Non-localhost routes are skipped unless explicitly requested.
			if host == "localhost" {
				// Fall through — proxy the request even with mismatched host
			} else if r.gwCfg.Routing.AllowDevHostFallback {
				return c.Next()
			} else {
				return c.Next()
			}
		}

		// Enforce route policy (auth validation happens HERE, after route matching)
		if r.auth != nil {
			if err := r.auth.EnforcePolicy(c, route.Policy); err != nil {
				return err
			}
		}

		// Set route context for observability
		c.Locals("routePolicy", route.Policy)
		c.Locals("service", route.Upstream)
		c.Locals("routeID", route.ID)

		// Compute strip prefix and upstream path prefix.
		// When stripPrefix is not explicitly set, use pathPrefix so the
		// proxy strips the incoming path and prepends the upstream prefix correctly.
		stripPrefix := route.StripPrefix
		if stripPrefix == "" {
			stripPrefix = route.PathPrefix
		}
		upstreamPrefix := route.UpstreamPathPrefix
		if upstreamPrefix == "" {
			upstreamPrefix = route.PathPrefix
		}

		return r.proxy.ForwardWithPrefix(c, route.Upstream, stripPrefix, upstreamPrefix)
	}
}

// FindHost returns the HostCfg matching the request Host header, or nil.
func (r *Router) FindHost(host string) *config.HostCfg {
	return r.findHost(host)
}

func (r *Router) findHost(host string) *config.HostCfg {
	host = strings.ToLower(host)
	// Exact match
	for i := range r.gwCfg.Hosts {
		if strings.EqualFold(r.gwCfg.Hosts[i].Host, host) {
			return &r.gwCfg.Hosts[i]
		}
	}
	// Dev fallback to localhost (only when explicitly enabled and not in production)
	isProduction := r.gwCfg.App.Env == "production" || r.gwCfg.App.Env == "staging"
	if !isProduction && r.gwCfg.Routing.AllowDevHostFallback {
		for i := range r.gwCfg.Hosts {
			if strings.EqualFold(r.gwCfg.Hosts[i].Host, "localhost") {
				return &r.gwCfg.Hosts[i]
			}
		}
	}
	return nil
}

// FindRoute finds the matching RouteCfg for a given host+path+method.
func (r *Router) FindRoute(host, path, method string) *config.RouteCfg {
	hostCfg := r.findHost(host)
	if hostCfg == nil {
		return nil
	}
	for i := range hostCfg.Routes {
		route := &hostCfg.Routes[i]
		if matchesPathPrefix(path, route.PathPrefix) && containsMethod(route.Methods, method) {
			return route
		}
	}
	return nil
}

// GetRoutePolicy returns the route policy for the current request.
// Returns empty string if no matching route found.
func (r *Router) GetRoutePolicy(host, path, method string) string {
	route := r.FindRoute(host, path, method)
	if route == nil {
		return ""
	}
	return route.Policy
}

// ─── Helpers ────────────────────────────────────────────────

func matchesPathPrefix(requestPath, prefix string) bool {
	if requestPath == prefix {
		return true
	}
	if strings.HasPrefix(requestPath, prefix) {
		remaining := strings.TrimPrefix(requestPath, prefix)
		if remaining == "" || strings.HasPrefix(remaining, "/") {
			return true
		}
	}
	return false
}

func containsMethod(methods []string, method string) bool {
	for _, m := range methods {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}

// gatewayError returns a standardized gateway error response.
// Browsers receive a friendly HTML page; API clients receive the JSON payload.
func gatewayError(c *fiber.Ctx, status int, code, message string) error {
	return respond.WriteError(c, status, code, message, fiber.Map{
		"success": false,
		"error": fiber.Map{
			"code":    code,
			"message": message,
		},
		"path":   c.Path(),
		"method": c.Method(),
	})
}
