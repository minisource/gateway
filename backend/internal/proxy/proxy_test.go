package proxy

import (
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// newTestProxy builds a ServiceProxy with a single "test" service client.
// The service URL points at an unroutable port so any probe fails fast.
func newTestProxy() *ServiceProxy {
	p := &ServiceProxy{
		services: make(map[string]*ServiceClient),
	}
	p.services["test"] = &ServiceClient{
		Name:    "test",
		URL:     "http://127.0.0.1:1",
		Healthy: true,
		Client:  &fasthttp.Client{ReadTimeout: 15 * time.Second},
	}
	return p
}

func TestHealthCheckRequiresConsecutiveFailures(t *testing.T) {
	p := newTestProxy()

	// First failure alone must NOT flip the service to unhealthy.
	p.recordHealth("test", false)
	if svc, _ := p.GetService("test"); !svc.Healthy {
		t.Fatal("service must stay healthy after a single failure (threshold=2)")
	}
	if svc, _ := p.GetService("test"); svc.HealthFailures != 1 {
		t.Fatalf("expected 1 consecutive failure, got %d", svc.HealthFailures)
	}

	// Second consecutive failure must flip it to unhealthy.
	p.recordHealth("test", false)
	if svc, _ := p.GetService("test"); svc.Healthy {
		t.Fatal("service must be unhealthy after 2 consecutive failures")
	}
}

func TestHealthCheckSuccessResetsFailures(t *testing.T) {
	p := newTestProxy()

	// Two failures → unhealthy.
	p.HealthCheck("test")
	p.HealthCheck("test")
	if svc, _ := p.GetService("test"); svc.Healthy {
		t.Fatal("expected unhealthy after 2 failures")
	}

	// A success must reset the counter and restore health.
	p.recordHealth("test", true)
	if svc, _ := p.GetService("test"); !svc.Healthy {
		t.Fatal("expected healthy after a successful probe")
	}
	if svc, _ := p.GetService("test"); svc.HealthFailures != 0 {
		t.Fatalf("expected failure counter reset to 0, got %d", svc.HealthFailures)
	}
}

func TestHealthCheckFailureCounterCappedAtThreshold(t *testing.T) {
	p := newTestProxy()

	// Three failures: counter keeps growing but state stays unhealthy.
	for i := 0; i < 3; i++ {
		p.recordHealth("test", false)
	}
	svc, _ := p.GetService("test")
	if svc.Healthy {
		t.Fatal("expected unhealthy after multiple failures")
	}
	if svc.HealthFailures != 3 {
		t.Fatalf("expected counter at 3, got %d", svc.HealthFailures)
	}
	// Still unhealthy after more failures.
	p.recordHealth("test", false)
	if svc, _ := p.GetService("test"); svc.Healthy {
		t.Fatal("expected service to remain unhealthy")
	}
}

func TestHealthCheckUnknownService(t *testing.T) {
	p := newTestProxy()
	if ok := p.HealthCheck("missing"); ok {
		t.Fatal("expected unhealthy result for unknown service")
	}
	if _, ok := p.GetService("missing"); ok {
		t.Fatal("unknown service must not be created by HealthCheck")
	}
}

// TestHealthCheckWiring verifies HealthCheck still routes probe results through
// recordHealth (single real-network probe to an unroutable port, fails fast).
func TestHealthCheckWiring(t *testing.T) {
	p := newTestProxy()
	// Probe to 127.0.0.1:1 refuses immediately on local hosts; the service URL
	// has no HealthPath set, so this exercises the full HealthCheck path.
	if ok := p.HealthCheck("test"); ok {
		t.Fatal("expected probe against unroutable port to fail")
	}
	if svc, _ := p.GetService("test"); svc.HealthFailures != 1 {
		t.Fatalf("expected HealthCheck to record 1 failure, got %d", svc.HealthFailures)
	}
}

func TestLastCheckUpdated(t *testing.T) {
	p := newTestProxy()
	before := time.Now().Add(-time.Hour)
	p.services["test"].LastCheck = before

	p.recordHealth("test", true)
	svc, _ := p.GetService("test")
	if !svc.LastCheck.After(before) {
		t.Fatal("LastCheck must be updated after a probe")
	}
}
