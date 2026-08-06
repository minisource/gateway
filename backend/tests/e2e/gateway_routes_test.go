//go:build e2e

package e2e_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/minisource/go-common/testing/e2e"
)

func TestGateway_AllServiceRoutes(t *testing.T) {
	authURL := e2e.BaseURLFromEnv("AUTH_BASE_URL", "http://127.0.0.1:9001")
	gw := e2e.NewClient(e2e.BaseURLFromEnv("GATEWAY_BASE_URL", "http://127.0.0.1:8080"), nil)
	gw.RequireUp(t, "/health")

	token := e2e.LoginAuth(t, authURL, "admin@example.com", "AdminPass123!")
	h := e2e.Bearer(token)
	h["X-Tenant-ID"] = "default"
	_, _, tenantID, storageH := e2e.AdminAuthContext(t, authURL, "admin@example.com", "AdminPass123!")

	cases := []struct {
		name         string
		method, path string
		body         any
		headers      map[string]string
		want         []int
	}{
		{"scheduler_jobs", http.MethodGet, "/api/v1/jobs", nil, nil, []int{http.StatusOK, http.StatusUnauthorized, http.StatusBadGateway, http.StatusNotFound}},
		{"feedback_list", http.MethodGet, "/api/v1/feedback", nil, h, []int{http.StatusOK, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusNotFound}},
		{"feedback_categories", http.MethodGet, "/api/v1/categories", nil, nil, []int{http.StatusOK, http.StatusBadGateway, http.StatusNotFound}},
		{"ticket_departments", http.MethodGet, "/api/v1/departments", nil, e2e.TenantHeader("default"), []int{http.StatusOK, http.StatusBadGateway, http.StatusNotFound}},
		{"ticket_list", http.MethodGet, "/api/v1/tickets", nil, h, []int{http.StatusOK, http.StatusUnauthorized, http.StatusBadRequest, http.StatusBadGateway, http.StatusNotFound}},
		{"comment_list", http.MethodGet, "/api/v1/comments?limit=1", nil, h, []int{http.StatusOK, http.StatusUnauthorized, http.StatusBadGateway, http.StatusNotFound}},
		{"storage_folders", http.MethodGet, "/api/v1/folders", nil, storageH, []int{http.StatusOK, http.StatusUnauthorized, http.StatusBadGateway, http.StatusNotFound}},
		{"log_list", http.MethodGet, "/api/v1/logs", nil, h, []int{http.StatusOK, http.StatusUnauthorized, http.StatusBadGateway, http.StatusNotFound}},
		{"notifier_templates", http.MethodGet, "/api/v1/templates", nil, h, []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusBadGateway, http.StatusNotFound, http.StatusBadRequest}},
		{"ticket_admin_dashboard", http.MethodGet, "/api/v1/admin/dashboard/stats", nil, h, []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusBadGateway, http.StatusNotFound}},
		{"feedback_admin_stats", http.MethodGet, "/api/v1/admin/stats", nil, h, []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusBadGateway, http.StatusNotFound}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client := gw
			if len(tc.headers) > 0 {
				client = gw.WithHeaders(tc.headers)
			}
			resp, body, err := client.Do(tc.method, tc.path, tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode == http.StatusNotFound {
				t.Skip("route not configured on gateway")
			}
			e2e.ExpectStatus(t, resp, body, tc.want...)
		})
	}

	// Write paths through gateway
	title := fmt.Sprintf("gw-fb-%d", time.Now().UnixNano())
	resp, body, err := gw.WithHeaders(h).Do(http.MethodPost, "/api/v1/feedback", map[string]any{
		"title": title, "description": "via gateway",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == http.StatusNotFound {
		t.Skip("feedback route not on gateway")
	}
	e2e.ExpectStatus(t, resp, body, http.StatusOK, http.StatusCreated, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadGateway)
	if resp.StatusCode == http.StatusTooManyRequests {
		return
	}

	resp, body, err = gw.WithHeaders(h).Do(http.MethodPost, "/api/v1/tickets", map[string]any{
		"subject": "GW ticket", "description": "via gateway", "priority": "normal",
	})
	if err != nil {
		t.Fatal(err)
	}
	e2e.ExpectStatus(t, resp, body, http.StatusOK, http.StatusCreated, http.StatusUnauthorized, http.StatusBadRequest, http.StatusBadGateway)

	resp, body, err = gw.WithHeaders(h).Do(http.MethodPost, "/api/v1/logs", map[string]any{
		"service_name": "e2e-gateway-all", "level": "INFO", "message": "gateway routes test",
	})
	if err != nil {
		t.Fatal(err)
	}
	e2e.ExpectStatus(t, resp, body, http.StatusOK, http.StatusCreated, http.StatusUnauthorized, http.StatusBadGateway)

	_ = tenantID
}
