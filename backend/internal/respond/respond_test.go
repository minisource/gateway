package respond

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
)

// jsonPayload mirrors the gateway's UPSTREAM_ERROR shape.
func jsonPayload() fiber.Map {
	return fiber.Map{
		"success": false,
		"error": fiber.Map{
			"code":    "UPSTREAM_ERROR",
			"message": "Upstream request failed",
			"details": "dial tcp4 127.0.0.1:3004: connection refused",
		},
	}
}

func TestBrowserRequestGetsHTML(t *testing.T) {
	app := fiber.New()
	app.Get("/err", func(c *fiber.Ctx) error {
		return WriteError(c, fiber.StatusBadGateway, "UPSTREAM_ERROR", "Upstream request failed", jsonPayload())
	})

	// Browser navigation: Accept text/html
	req := httptest.NewRequest("GET", "/err", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusBadGateway, resp.StatusCode)

	ct := resp.Header.Get("Content-Type")
	require.True(t, strings.Contains(ct, "text/html"), "expected HTML content type, got %q", ct)

	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	require.True(t, strings.Contains(html, "<!DOCTYPE html>"), "expected HTML document")
	require.True(t, strings.Contains(html, "Try again"), "expected retry button")
	// Internal dial details must NOT leak to browsers.
	require.False(t, strings.Contains(html, "127.0.0.1:3004"), "must not leak internal details to browsers")
}

func TestSecFetchModeNavigateGetsHTML(t *testing.T) {
	app := fiber.New()
	app.Get("/err", func(c *fiber.Ctx) error {
		return WriteError(c, fiber.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "service notifier-frontend is unavailable", jsonPayload())
	})

	req := httptest.NewRequest("GET", "/err", nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusServiceUnavailable, resp.StatusCode)

	ct := resp.Header.Get("Content-Type")
	require.True(t, strings.Contains(ct, "text/html"), "expected HTML content type, got %q", ct)
	require.Equal(t, "10", resp.Header.Get("Retry-After"))
}

func TestAPIClientKeepsJSONContract(t *testing.T) {
	app := fiber.New()
	app.Get("/err", func(c *fiber.Ctx) error {
		return WriteError(c, fiber.StatusBadGateway, "UPSTREAM_ERROR", "Upstream request failed", jsonPayload())
	})

	// API client: Accept application/json
	req := httptest.NewRequest("GET", "/err", nil)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusBadGateway, resp.StatusCode)

	ct := resp.Header.Get("Content-Type")
	require.True(t, strings.Contains(ct, "application/json"), "expected JSON content type, got %q", ct)

	// Body must keep the exact original JSON shape (including details for API clients).
	body, _ := io.ReadAll(resp.Body)
	jsonBody := string(body)
	require.True(t, strings.Contains(jsonBody, `"code":"UPSTREAM_ERROR"`), "JSON shape changed: %s", jsonBody)
	require.True(t, strings.Contains(jsonBody, "127.0.0.1:3004"), "API clients must keep details for debugging")
}

func TestNoAcceptHeaderDefaultsToJSON(t *testing.T) {
	app := fiber.New()
	app.Get("/err", func(c *fiber.Ctx) error {
		return WriteError(c, fiber.StatusNotFound, "ROUTE_NOT_FOUND", "Route not found: GET /x", jsonPayload())
	})

	// No Accept header at all — treat as API client (curl, health checks, etc.)
	req := httptest.NewRequest("GET", "/err", nil)
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusNotFound, resp.StatusCode)
	ct := resp.Header.Get("Content-Type")
	require.True(t, strings.Contains(ct, "application/json"), "expected JSON content type, got %q", ct)
}

func TestPersianBrowserGetsLocalizedHTML(t *testing.T) {
	app := fiber.New()
	app.Get("/err", func(c *fiber.Ctx) error {
		return WriteError(c, fiber.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", T(c, "errors.service_unavailable"), jsonPayload())
	})

	req := httptest.NewRequest("GET", "/err", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "fa")
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusServiceUnavailable, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	// Persian: RTL document + translated strings.
	require.True(t, strings.Contains(html, `lang="fa" dir="rtl"`), "expected fa/rtl html tag, got: %s", html)
	require.True(t, strings.Contains(html, "تلاش مجدد"), "expected Persian retry button")
	require.True(t, strings.Contains(html, "سرویس به‌طور موقت در دسترس نیست"), "expected Persian heading")
}

func TestPersianAPIClientGetsLocalizedJSON(t *testing.T) {
	app := fiber.New()
	app.Get("/err", func(c *fiber.Ctx) error {
		msg := T(c, "errors.service_unavailable")
		return WriteError(c, fiber.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", msg, fiber.Map{
			"success": false,
			"error": fiber.Map{
				"code":    "SERVICE_UNAVAILABLE",
				"message": msg,
			},
		})
	})

	req := httptest.NewRequest("GET", "/err", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "fa-IR, fa;q=0.9, en;q=0.8")
	resp, err := app.Test(req, -1)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusServiceUnavailable, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	jsonBody := string(body)
	require.True(t, strings.Contains(jsonBody, "سرویس به‌طور موقت در دسترس نیست"), "expected Persian message, got: %s", jsonBody)
	require.True(t, strings.Contains(jsonBody, `"code":"SERVICE_UNAVAILABLE"`), "code must stay machine-readable: %s", jsonBody)
}

func TestEnglishDefaultFallback(t *testing.T) {
	app := fiber.New()
	app.Get("/err", func(c *fiber.Ctx) error {
		return WriteError(c, fiber.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", T(c, "errors.service_unavailable"), jsonPayload())
	})

	// No Accept-Language → English default.
	req := httptest.NewRequest("GET", "/err", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := app.Test(req, -1)
	require.NoError(t, err)

	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	require.True(t, strings.Contains(html, `lang="en" dir="ltr"`), "expected en/ltr html tag, got: %s", html)
	require.True(t, strings.Contains(html, "Try again"), "expected English retry button")
}

func TestIsBrowserRequest(t *testing.T) {
	cases := []struct {
		name string
		set  func(r *http.Request)
		want bool
	}{
		{
			name: "Accept text/html",
			set: func(r *http.Request) {
				r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
			},
			want: true,
		},
		{
			name: "Sec-Fetch-Mode navigate wins",
			set: func(r *http.Request) {
				r.Header.Set("Accept", "*/*")
				r.Header.Set("Sec-Fetch-Mode", "navigate")
			},
			want: true,
		},
		{
			name: "API fetch Accept */*",
			set:  func(r *http.Request) { r.Header.Set("Accept", "*/*") },
			want: false,
		},
		{
			name: "API JSON Accept",
			set:  func(r *http.Request) { r.Header.Set("Accept", "application/json") },
			want: false,
		},
		{
			name: "no Accept",
			set:  func(r *http.Request) {},
			want: false,
		},
	}

	app := fiber.New()
	// Echo handler so we can observe IsBrowserRequest through the real Ctx.
	app.Get("/check", func(c *fiber.Ctx) error {
		if IsBrowserRequest(c) {
			return c.SendString("browser")
		}
		return c.SendString("api")
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/check", nil)
			tc.set(req)
			resp, err := app.Test(req, -1)
			require.NoError(t, err)
			body, _ := io.ReadAll(resp.Body)
			if tc.want {
				require.Equal(t, "browser", string(body), "mismatch for %s", tc.name)
			} else {
				require.Equal(t, "api", string(body), "mismatch for %s", tc.name)
			}
		})
	}
}
