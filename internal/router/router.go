package router

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/config"
	"github.com/minisource/gateway/internal/proxy"
)

// Router manages API gateway routing
type Router struct {
	app    *fiber.App
	proxy  *proxy.ServiceProxy
	routes *config.RouteConfig
	cfg    *config.Config
}

// New creates a new router
func New(app *fiber.App, proxy *proxy.ServiceProxy, routes *config.RouteConfig, cfg *config.Config) *Router {
	return &Router{
		app:    app,
		proxy:  proxy,
		routes: routes,
		cfg:    cfg,
	}
}

// SetupRoutes configures all routes
func (r *Router) SetupRoutes() {
	// Setup routes from configuration
	for _, route := range r.routes.Routes {
		r.setupRoute(route)
	}

	// Catch-all for unmatched routes
	r.app.Use(func(c *fiber.Ctx) error {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error":   "not_found",
			"message": "The requested resource was not found",
			"path":    c.Path(),
		})
	})
}

// setupRoute configures a single route
func (r *Router) setupRoute(route config.Route) {
	// Handle gateway internal routes
	if route.Service == "gateway" {
		return // These are handled by health/metrics handlers
	}

	// Create route pattern (supports wildcards)
	pattern := route.Path
	if !strings.HasSuffix(pattern, "*") {
		pattern = pattern + "/*"
	}

	handler := r.createProxyHandler(route)

	// Register for all specified methods
	for _, method := range route.Methods {
		switch strings.ToUpper(method) {
		case "GET":
			r.app.Get(pattern, handler)
			r.app.Get(route.Path, handler) // Exact match
		case "POST":
			r.app.Post(pattern, handler)
			r.app.Post(route.Path, handler)
		case "PUT":
			r.app.Put(pattern, handler)
			r.app.Put(route.Path, handler)
		case "DELETE":
			r.app.Delete(pattern, handler)
			r.app.Delete(route.Path, handler)
		case "PATCH":
			r.app.Patch(pattern, handler)
			r.app.Patch(route.Path, handler)
		case "OPTIONS":
			r.app.Options(pattern, handler)
			r.app.Options(route.Path, handler)
		}
	}
}

// createProxyHandler creates a handler that proxies to the target service
func (r *Router) createProxyHandler(route config.Route) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Store route info in context for middleware
		c.Locals("route", route)
		c.Locals("isPublic", route.Public)
		c.Locals("service", route.Service)

		// Determine strip prefix
		stripPrefix := ""
		if route.StripPrefix {
			stripPrefix = route.Path
		}

		return r.proxy.Forward(c, route.Service, stripPrefix)
	}
}

// IsPublicRoute checks if a path is a public route
func (r *Router) IsPublicRoute(path string, method string) bool {
	for _, route := range r.routes.Routes {
		if matchesPath(path, route.Path) && containsMethod(route.Methods, method) {
			return route.Public
		}
	}
	return false
}

// GetRouteForPath returns the route config for a given path
func (r *Router) GetRouteForPath(path string, method string) *config.Route {
	for _, route := range r.routes.Routes {
		if matchesPath(path, route.Path) && containsMethod(route.Methods, method) {
			return &route
		}
	}
	return nil
}

// matchesPath checks if a request path matches a route pattern
func matchesPath(requestPath, routePath string) bool {
	// Exact match
	if requestPath == routePath {
		return true
	}

	// Prefix match (route path is prefix of request path)
	if strings.HasPrefix(requestPath, routePath) {
		// Make sure it's a proper prefix (followed by / or end)
		remaining := strings.TrimPrefix(requestPath, routePath)
		if remaining == "" || strings.HasPrefix(remaining, "/") {
			return true
		}
	}

	return false
}

// containsMethod checks if a method is in the allowed methods list
func containsMethod(methods []string, method string) bool {
	for _, m := range methods {
		if strings.EqualFold(m, method) {
			return true
		}
	}
	return false
}
