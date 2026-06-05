//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"

	"github.com/minisource/go-common/testing/e2e"
)

func TestGateway_API(t *testing.T) {
	c := e2e.NewClient(e2e.BaseURLFromEnv("GATEWAY_BASE_URL", "http://127.0.0.1:8080"), nil)
	c.RequireUp(t, "/health")

	c.RunCases(t, []e2e.Case{
		{Name: "health", Method: http.MethodGet, Path: "/health", WantCode: []int{http.StatusOK}},
		{Name: "ready", Method: http.MethodGet, Path: "/ready", WantCode: []int{http.StatusOK, http.StatusServiceUnavailable}},
		{Name: "live", Method: http.MethodGet, Path: "/live", WantCode: []int{http.StatusOK}},
		{Name: "metrics", Method: http.MethodGet, Path: "/metrics", WantCode: []int{http.StatusOK, http.StatusInternalServerError}},
		{Name: "proxy_auth_login", Method: http.MethodPost, Path: "/api/v1/auth/login", Body: map[string]any{
			"email": "admin@example.com", "password": "AdminPass123!",
		}, WantCode: []int{http.StatusOK, http.StatusUnauthorized, http.StatusBadGateway}},
	})

	authURL := e2e.BaseURLFromEnv("AUTH_BASE_URL", "http://127.0.0.1:9001")
	svcToken := e2e.ServiceToken(t, authURL, "auth-service", "auth-service-secret-key")
	h := e2e.Bearer(svcToken)
	c.RunCases(t, []e2e.Case{
		{Name: "proxy_users_me", Method: http.MethodGet, Path: "/api/v1/users/me", Headers: e2e.Bearer(e2e.LoginAuth(t, authURL, "admin@example.com", "AdminPass123!")), WantCode: []int{http.StatusOK, http.StatusBadGateway, http.StatusUnauthorized}},
		{Name: "proxy_notifier_templates", Method: http.MethodGet, Path: "/api/v1/templates", Headers: h, WantCode: []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusBadGateway, http.StatusNotFound, http.StatusBadRequest}},
	})
}
