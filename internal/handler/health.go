package handler

import (
	"runtime"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/internal/proxy"
)

var startTime = time.Now()

// HealthHandler handles health check endpoints
type HealthHandler struct {
	proxy *proxy.ServiceProxy
}

// NewHealthHandler creates a new health handler
func NewHealthHandler(proxy *proxy.ServiceProxy) *HealthHandler {
	return &HealthHandler{
		proxy: proxy,
	}
}

// RegisterRoutes registers health check routes
func (h *HealthHandler) RegisterRoutes(app *fiber.App) {
	app.Get("/health", h.Health)
	app.Get("/ready", h.Ready)
	app.Get("/live", h.Live)
	app.Get("/health/services", h.ServicesHealth)
}

// Health returns overall gateway health
func (h *HealthHandler) Health(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status":    "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"uptime":    time.Since(startTime).String(),
		"version":   "1.0.0",
	})
}

// Ready checks if gateway is ready to serve traffic
func (h *HealthHandler) Ready(c *fiber.Ctx) error {
	services := h.proxy.GetServicesHealth()
	allHealthy := true

	for _, healthy := range services {
		if !healthy {
			allHealthy = false
			break
		}
	}

	status := fiber.StatusOK
	readyStatus := "ready"
	if !allHealthy {
		status = fiber.StatusServiceUnavailable
		readyStatus = "not_ready"
	}

	return c.Status(status).JSON(fiber.Map{
		"status":    readyStatus,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"services":  services,
	})
}

// Live returns liveness status (for k8s liveness probe)
func (h *HealthHandler) Live(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status":    "alive",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// ServicesHealth returns detailed health of all services
func (h *HealthHandler) ServicesHealth(c *fiber.Ctx) error {
	services := h.proxy.GetServicesHealth()

	details := make(map[string]fiber.Map)
	for name, healthy := range services {
		status := "healthy"
		if !healthy {
			status = "unhealthy"
		}
		details[name] = fiber.Map{
			"status":  status,
			"healthy": healthy,
		}
	}

	return c.JSON(fiber.Map{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"services":  details,
		"memory":    getMemoryStats(),
	})
}

// getMemoryStats returns current memory statistics
func getMemoryStats() fiber.Map {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return fiber.Map{
		"alloc_mb":       bToMb(m.Alloc),
		"total_alloc_mb": bToMb(m.TotalAlloc),
		"sys_mb":         bToMb(m.Sys),
		"num_gc":         m.NumGC,
	}
}

func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}
