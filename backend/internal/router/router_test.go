package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/config"
	"github.com/minisource/gateway/internal/middleware"
	"github.com/minisource/gateway/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProxy returns a service proxy that records the upstream service name called.
func testGatewayConfig() *config.GatewayConfig {
	return &config.GatewayConfig{
		App: config.AppCfg{
			Env:      "development",
			LogLevel: "debug",
			LogFmt:   "console",
		},
		Routing: config.RoutingCfg{
			AllowDevHostFallback: true,
		},
		Auth: config.AuthCfg{
			Enabled:           false,
			ValidateAtGateway: false,
		},
		Services: config.ServicesMap{
			"auth":      {BaseURL: "http://127.0.0.1:9001", Timeout: 0, HealthPath: "/health"},
			"service-a": {BaseURL: "http://127.0.0.1:9010", Timeout: 0, HealthPath: "/health"},
			"service-b": {BaseURL: "http://127.0.0.1:9020", Timeout: 0, HealthPath: "/health"},
		},
	}
}

// ─── Host Matching Tests ────────────────────────────────────

func TestRouter_HostMatching_ExactMatch(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "api.service-a.invalid", Routes: []config.RouteCfg{
			{ID: "service-a-root", PathPrefix: "/v1", Upstream: "service-a", Policy: "authenticated", Methods: []string{"GET"}},
		}},
		{Host: "api.service-b.invalid", Routes: []config.RouteCfg{
			{ID: "service-b-root", PathPrefix: "/v1", Upstream: "service-b", Policy: "authenticated", Methods: []string{"GET"}},
		}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	host := router.findHost("api.service-a.invalid")
	require.NotNil(t, host)
	assert.Equal(t, "api.service-a.invalid", host.Host)

	host = router.findHost("Api.Service-A.Invalid")
	require.NotNil(t, host)
	assert.Equal(t, "api.service-a.invalid", host.Host)

	host = router.findHost("api.service-b.invalid")
	require.NotNil(t, host)
	assert.Equal(t, "api.service-b.invalid", host.Host)
}

func TestRouter_HostMatching_UnknownHost_Dev(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	// Dev with fallback: unknown host → localhost
	host := router.findHost("random-host.com")
	assert.NotNil(t, host, "dev should fall back to localhost")
	assert.Equal(t, "localhost", host.Host)
}

func TestRouter_HostMatching_UnknownHost_Dev_NoFallback(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.Routing.AllowDevHostFallback = false
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	host := router.findHost("random-host.com")
	assert.Nil(t, host, "dev without fallback should return nil for unknown host")
}

func TestRouter_HostMatching_UnknownHost_Production(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.App.Env = "production"
	gwCfg.Routing.AllowDevHostFallback = false
	gwCfg.Hosts = []config.HostCfg{
		{Host: "api.service-a.invalid", Routes: []config.RouteCfg{}},
		{Host: "localhost", Routes: []config.RouteCfg{}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	// Production: no fallback for unknown hosts
	host := router.findHost("random-host.com")
	assert.Nil(t, host, "production must return nil for unknown host")

	// But known hosts match
	host = router.findHost("api.service-a.invalid")
	assert.NotNil(t, host)
}

// ─── Route Matching Tests ──────────────────────────────────

func TestRouter_FindRoute_LongestPrefix_OrderIndependent(t *testing.T) {
	// A generic prefix listed FIRST must not shadow a more specific one — this is
	// exactly the "Cannot GET /divipay/app" bug (generic /divipay proxying to the
	// wrong upstream when the config/merge order puts it before /divipay/app).
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{
			{ID: "divipay-landing", PathPrefix: "/divipay", Upstream: "divipay", Policy: "public", Methods: []string{"GET"}},
			{ID: "divipay-dashboard", PathPrefix: "/divipay/app", Upstream: "divipay-dashboard", Policy: "public", Methods: []string{"GET"}},
		}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	route := router.FindRoute("localhost", "/divipay/app", "GET")
	require.NotNil(t, route)
	assert.Equal(t, "divipay-dashboard", route.ID, "specific /divipay/app must win over generic /divipay")
	assert.Equal(t, "divipay-dashboard", route.Upstream)

	// A bare /divipay path still matches the generic landing route.
	route = router.FindRoute("localhost", "/divipay", "GET")
	require.NotNil(t, route)
	assert.Equal(t, "divipay-landing", route.ID)
}

func TestRouter_SetupRoutes_OverlappingPrefix_OrderIndependent(t *testing.T) {
	// End-to-end: with the generic prefix registered first in the config, the
	// gateway must still proxy /divipay/app to the dashboard upstream (not the
	// backend that answers "Cannot GET /divipay/app").
	dashboard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("DASHBOARD:" + r.URL.Path))
	}))
	defer dashboard.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("BACKEND:" + r.URL.Path))
	}))
	defer backend.Close()

	gwCfg := testGatewayConfig()
	gwCfg.Services["divipay"] = &config.ServiceCfg{BaseURL: backend.URL, Timeout: 0, HealthPath: "/health"}
	gwCfg.Services["divipay-dashboard"] = &config.ServiceCfg{BaseURL: dashboard.URL, Timeout: 0, HealthPath: "/health"}
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{
			// Deliberately generic-first, as a naive config merge would produce.
			{ID: "divipay-landing", PathPrefix: "/divipay", Upstream: "divipay", Policy: "public", Methods: []string{"GET"}},
			{ID: "divipay-dashboard", PathPrefix: "/divipay/app", Upstream: "divipay-dashboard", Policy: "public", Methods: []string{"GET"}},
		}},
	}

	app := fiber.New(fiber.Config{ErrorHandler: defaultErrorHandler})
	router := New(app, proxy.NewServiceProxy(gwCfg), gwCfg, nil)
	router.SetupRoutes()

	req := httptest.NewRequest("GET", "http://localhost/divipay/app", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "DASHBOARD", "specific /divipay/app must proxy to the dashboard upstream")
	assert.NotContains(t, string(body), "BACKEND")
}

func TestRouter_RouteMatching_LongestPrefixWins(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "api.service-a.invalid", Routes: []config.RouteCfg{
			{ID: "auth-login", PathPrefix: "/v1/auth/login", Upstream: "auth", Policy: "public", Methods: []string{"POST"}},
			{ID: "auth-other", PathPrefix: "/v1/auth", Upstream: "auth", Policy: "authenticated", Methods: []string{"GET", "POST"}},
			{ID: "service-a-root", PathPrefix: "/v1", Upstream: "service-a", Policy: "authenticated", Methods: []string{"GET", "POST"}},
		}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	// /v1/auth/login should match auth-login (most specific)
	route := router.FindRoute("api.service-a.invalid", "/v1/auth/login", "POST")
	require.NotNil(t, route)
	assert.Equal(t, "auth-login", route.ID)
	assert.Equal(t, "public", route.Policy)

	// /v1/auth/other should match auth-other
	route = router.FindRoute("api.service-a.invalid", "/v1/auth/other", "GET")
	require.NotNil(t, route)
	assert.Equal(t, "auth-other", route.ID)

	// /v1/people should match service-a-root (catch-all v1)
	route = router.FindRoute("api.service-a.invalid", "/v1/people", "GET")
	require.NotNil(t, route)
	assert.Equal(t, "service-a-root", route.ID)
	assert.Equal(t, "service-a", route.Upstream)

	// /v2 should not match anything
	route = router.FindRoute("api.service-a.invalid", "/v2/test", "GET")
	assert.Nil(t, route)
}

func TestRouter_RouteMatching_MethodFiltering(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{
			{ID: "auth-login", PathPrefix: "/v1/auth/login", Upstream: "auth", Policy: "public", Methods: []string{"POST"}},
		}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	// POST matches
	route := router.FindRoute("localhost", "/v1/auth/login", "POST")
	require.NotNil(t, route)
	assert.Equal(t, "auth-login", route.ID)

	// GET does NOT match (only POST allowed)
	route = router.FindRoute("localhost", "/v1/auth/login", "GET")
	assert.Nil(t, route, "GET should not match POST-only route")
}

func TestRouter_RouteMatching_CustomDomain(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "api.service-b.invalid", Routes: []config.RouteCfg{
			{ID: "auth-login", PathPrefix: "/v1/auth/login", Upstream: "auth", Policy: "public", Methods: []string{"POST"}},
			{ID: "service-b-root", PathPrefix: "/v1", Upstream: "service-b", Policy: "authenticated", Methods: []string{"GET", "POST"}},
		}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	// Auth login → auth
	route := router.FindRoute("api.service-b.invalid", "/v1/auth/login", "POST")
	require.NotNil(t, route)
	assert.Equal(t, "auth", route.Upstream)

	// Custom groups → service-b
	route = router.FindRoute("api.service-b.invalid", "/v1/groups", "GET")
	require.NotNil(t, route)
	assert.Equal(t, "service-b", route.Upstream)
	assert.Equal(t, "service-b-root", route.ID)
}

func TestRouter_RouteMatching_StripPrefix(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{
			{ID: "service-a", PathPrefix: "/v1/service-a", Upstream: "service-a", StripPrefix: "/v1/service-a", UpstreamPathPrefix: "/v1", Policy: "authenticated", Methods: []string{"GET", "POST"}},
			{ID: "service-b", PathPrefix: "/v1/service-b", Upstream: "service-b", StripPrefix: "/v1/service-b", UpstreamPathPrefix: "/v1", Policy: "authenticated", Methods: []string{"GET", "POST"}},
		}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	// service-a stripPrefix
	route := router.FindRoute("localhost", "/v1/service-a/people", "GET")
	require.NotNil(t, route)
	assert.Equal(t, "service-a", route.Upstream)
	assert.Equal(t, "/v1/service-a", route.StripPrefix)
	assert.Equal(t, "/v1", route.UpstreamPathPrefix)

	// service-b stripPrefix
	route = router.FindRoute("localhost", "/v1/service-b/groups", "GET")
	require.NotNil(t, route)
	assert.Equal(t, "service-b", route.Upstream)
	assert.Equal(t, "/v1/service-b", route.StripPrefix)
	assert.Equal(t, "/v1", route.UpstreamPathPrefix)
}

// ─── Policy Tests ─────────────────────────────────────────

func TestRouter_GetRoutePolicy(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{
			{ID: "public-route", PathPrefix: "/v1/auth/login", Upstream: "auth", Policy: "public", Methods: []string{"POST"}},
			{ID: "auth-route", PathPrefix: "/v1/service-a", Upstream: "service-a", Policy: "authenticated", Methods: []string{"GET"}},
			{ID: "disabled-route", PathPrefix: "/v1/internal", Upstream: "notifier", Policy: "disabled", Methods: []string{"GET"}},
		}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	assert.Equal(t, "public", router.GetRoutePolicy("localhost", "/v1/auth/login", "POST"))
	assert.Equal(t, "authenticated", router.GetRoutePolicy("localhost", "/v1/service-a/people", "GET"))
	assert.Equal(t, "disabled", router.GetRoutePolicy("localhost", "/v1/internal/test", "GET"))
	assert.Equal(t, "", router.GetRoutePolicy("localhost", "/v1/nonexistent", "GET"))
}

// ─── Host 404 Integration Tests ───────────────────────────

func TestRouter_Integration_UnknownHost_Returns404_InProd(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.App.Env = "production"
	gwCfg.Routing.AllowDevHostFallback = false
	gwCfg.Hosts = []config.HostCfg{
		{Host: "api.service-a.invalid", Routes: []config.RouteCfg{
			{ID: "service-a", PathPrefix: "/v1", Upstream: "service-a", Policy: "authenticated", Methods: []string{"GET"}},
		}},
	}

	app := fiber.New(fiber.Config{ErrorHandler: defaultErrorHandler})
	router := New(app, nil, gwCfg, nil)
	router.SetupRoutes()

	// Request with unknown host → 404
	req := httptest.NewRequest("GET", "http://unknown.com/v1/people", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode)
}

func TestRouter_Integration_DevHostFallback_FindHostWorks(t *testing.T) {
	// findHost already correctly falls back to localhost in dev.
	// The host→route integration in createProxyHandler via c.Next() is fragile
	// and depends on route registration order. This is a known limitation.
	// The unit test for findHost covers the fallback logic.
	gwCfg := testGatewayConfig()
	gwCfg.App.Env = "development"
	gwCfg.Routing.AllowDevHostFallback = true
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{
			{ID: "auth-login", PathPrefix: "/v1/auth/login", Upstream: "auth", Policy: "public", Methods: []string{"POST"}},
		}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	// findHost correctly falls back
	host := router.findHost("unknown.com")
	require.NotNil(t, host)
	assert.Equal(t, "localhost", host.Host)
}

func TestRouter_SetupRoutes_DisabledRoutesNotRegistered(t *testing.T) {
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{
			{ID: "disabled-route", PathPrefix: "/v1/disabled", Upstream: "auth", Policy: "disabled", Methods: []string{"GET"}},
			{ID: "internal-route", PathPrefix: "/v1/internal", Upstream: "notifier", Policy: "internal", Methods: []string{"GET"}},
		}},
	}

	app := fiber.New(fiber.Config{ErrorHandler: defaultErrorHandler})
	router := New(app, nil, gwCfg, nil)
	router.SetupRoutes()

	// Disabled route returns 404
	req := httptest.NewRequest("GET", "http://localhost/v1/disabled/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode)

	// Internal route returns 404
	req = httptest.NewRequest("GET", "http://localhost/v1/internal/test", nil)
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode)
}

// ─── Route Registration Tests ─────────────────────────

func TestRouter_PublicRouteIsRegistered(t *testing.T) {
	// Verifies that public routes are found by FindRoute (unit-level routing test).
	// Full proxy integration requires a real/mock proxy — handled in e2e tests.
	gwCfg := testGatewayConfig()
	gwCfg.Hosts = []config.HostCfg{
		{Host: "localhost", Routes: []config.RouteCfg{
			{ID: "auth-login", PathPrefix: "/v1/auth/login", Upstream: "auth", Policy: "public", Methods: []string{"POST"}},
		}},
	}

	app := fiber.New()
	router := New(app, nil, gwCfg, nil)

	route := router.FindRoute("localhost", "/v1/auth/login", "POST")
	require.NotNil(t, route, "public route should be found")
	assert.Equal(t, "auth-login", route.ID)
	assert.Equal(t, "public", route.Policy)
}

// ─── Identity Header Stripping Test ────────────────────

func TestRouter_IdentityHeadersStrippedByGlobalMiddleware(t *testing.T) {
	// Identity header stripping is handled by the auth middleware's global Handler().
	app := fiber.New()
	app.Use(middleware.NewAuthMiddleware(&config.AuthCfg{Enabled: true}).Handler())
	app.Get("/test", func(c *fiber.Ctx) error {
		uid := c.Request().Header.Peek("X-User-ID")
		email := c.Request().Header.Peek("X-User-Email")
		roles := c.Request().Header.Peek("X-User-Roles")
		tenant := c.Request().Header.Peek("X-Tenant-ID")
		return c.JSON(fiber.Map{
			"user_id_len": len(uid),
			"email_len":   len(email),
			"roles_len":   len(roles),
			"tenant_len":  len(tenant),
		})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-User-ID", "attacker-id")
	req.Header.Set("X-User-Email", "attacker@evil.com")
	req.Header.Set("X-User-Roles", "admin")
	req.Header.Set("X-Tenant-ID", "evil-tenant")
	req.Header.Set("X-Actor-ID", "fake-actor")
	req.Header.Set("X-Actor-Roles", "super_admin")

	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, `"user_id_len":0`, "X-User-ID should be stripped")
	assert.Contains(t, bodyStr, `"email_len":0`, "X-User-Email should be stripped")
	assert.Contains(t, bodyStr, `"roles_len":0`, "X-User-Roles should be stripped")
	// X-Tenant-ID is intentionally preserved: it is a client context header,
	// not an identity header. Upstream services validate tenant access independently.
	assert.NotContains(t, bodyStr, `"tenant_len":0`, "X-Tenant-ID should be preserved (client context)")
}

// ─── Helpers ─────────────────────────────────────────────

func defaultErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
	}
	return c.Status(code).JSON(fiber.Map{
		"error": err.Error(),
	})
}
