package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/swagger"
	"github.com/minisource/gateway/config"
	_ "github.com/minisource/gateway/docs"
	"github.com/minisource/gateway/internal/handler"
	"github.com/minisource/gateway/internal/middleware"
	"github.com/minisource/gateway/internal/proxy"
	"github.com/minisource/gateway/internal/respond"
	"github.com/minisource/gateway/internal/router"
	commonMiddleware "github.com/minisource/go-common/http/middleware"
	commonTracing "github.com/minisource/go-common/tracing"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// @title MiniSource Gateway API
// @version 2.0
// @description Config-driven, multi-domain API Gateway for MiniSource microservices
// @host localhost:9000
// @schemes http https
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization

func main() {
	// Determine config path
	configPath := os.Getenv("GATEWAY_CONFIG")
	if configPath == "" {
		configPath = "config/config.yaml"
	}

	// Load gateway configuration (YAML + env overrides)
	gwCfg, err := config.LoadGatewayConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Production safety validation — reject unsafe config
	if err := gwCfg.ValidateProduction(); err != nil {
		log.Fatalf("Production safety check failed: %v", err)
	}

	// Setup logger
	logger := middleware.NewLogger(config.LoggingConfig{
		Level:  gwCfg.App.LogLevel,
		Format: gwCfg.App.LogFmt,
	})
	logger.Info("Starting MiniSource API Gateway v2.0", "env", gwCfg.App.Env)

	// Initialize tracing
	tracingCfg := commonTracing.LoadConfigFromEnv()
	if tracingCfg.Enabled {
		tp, err := commonTracing.InitTracer(context.Background(), tracingCfg)
		if err != nil {
			logger.Error("Failed to initialize tracing", "error", err)
		} else {
			defer func() {
				if err := tp.Shutdown(context.Background()); err != nil {
					logger.Error("Error shutting down tracer", "error", err)
				}
			}()
			logger.Info("Tracing initialized successfully", "endpoint", tracingCfg.CollectorURL)
		}
	}

	// Initialize service proxy
	serviceProxy := proxy.NewServiceProxy(gwCfg)
	serviceProxy.StartHealthChecks(30 * time.Second)

	// Initialize rate limiter
	rateLimiter := middleware.NewRateLimiter(config.RateLimitConfig{
		Enabled:        gwCfg.RateLimit.Enabled,
		RequestsPerSec: gwCfg.RateLimit.RequestsPerMinute / 60, // convert per-minute to per-second
		BurstSize:      gwCfg.RateLimit.Burst,
	}, nil)

	// Create Fiber app
	app := fiber.New(fiber.Config{
		ReadTimeout:             gwCfg.Server.ReadTimeout,
		WriteTimeout:            gwCfg.Server.WriteTimeout,
		IdleTimeout:             gwCfg.Server.IdleTimeout,
		AppName:                 gwCfg.App.Name + " v2.0.0",
		ServerHeader:            "",
		EnableTrustedProxyCheck: true,
		TrustedProxies:          gwCfg.Server.TrustedProxies,
		BodyLimit:               gwCfg.Server.MaxBodyBytes,
		ErrorHandler:            gatewayErrorHandler,
	})

	// Create auth middleware once (shared by global middleware and route-level enforcement)
	var authMiddleware *middleware.AuthMiddleware
	if gwCfg.Auth.Enabled {
		authMiddleware = middleware.NewAuthMiddleware(&gwCfg.Auth)
	}

	// Middleware stack (order matters)
	setupMiddleware(app, gwCfg, logger, rateLimiter, authMiddleware, tracingCfg.Enabled, tracingCfg.ServiceName)

	// Gateway-internal routes
	healthHandler := handler.NewHealthHandler(serviceProxy)
	healthHandler.RegisterRoutes(app)

	// Swagger & Metrics — DISABLED in production/staging for security
	isProduction := gwCfg.App.Env == "production" || gwCfg.App.Env == "staging"
	if !isProduction {
		app.Get("/metrics", adaptor.HTTPHandler(promhttp.HandlerFor(middleware.GetMetricsRegistry(), promhttp.HandlerOpts{
			// ContinueOnError: tolerate duplicate label sets from external
			// probes (e.g. odd-method health checks) instead of returning 500.
			ErrorHandling: promhttp.ContinueOnError,
		})))
		app.Get("/swagger/*", swagger.HandlerDefault)
		logger.Info("Swagger & Metrics endpoints enabled (non-production mode)")
	} else {
		// Return 404 for swagger/metrics in production
		app.Get("/swagger/*", func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		})
		app.Get("/metrics", func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "not found"})
		})
		logger.Info("Swagger & Metrics endpoints DISABLED (production mode)")
	}

	// Host-based proxy routes (auth enforcement at route handler level via EnforcePolicy)
	gatewayRouter := router.New(app, serviceProxy, gwCfg, authMiddleware)
	gatewayRouter.SetupRoutes()

	// Start server
	go func() {
		addr := fmt.Sprintf("%s:%s", gwCfg.Server.Host, gwCfg.Server.Port)
		logger.Info("Gateway listening", "address", addr)
		if err := app.Listen(addr); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down gateway...")

	ctx, cancel := context.WithTimeout(context.Background(), gwCfg.Server.ShutdownTimeout)
	defer cancel()

	if err := app.ShutdownWithContext(ctx); err != nil {
		logger.Error("Server forced to shutdown", "error", err)
	}

	_ = serviceProxy.Close()
	logger.Info("Gateway stopped")
}

func setupMiddleware(
	app *fiber.App,
	cfg *config.GatewayConfig,
	logger *middleware.SimpleLogger,
	rateLimiter *middleware.RateLimiter,
	auth *middleware.AuthMiddleware,
	tracingEnabled bool,
	serviceName string,
) {
	// Recovery
	app.Use(recover.New(recover.Config{EnableStackTrace: true}))

	// Request ID — runs before tracing so spans carry request.id
	app.Use(commonMiddleware.RequestID())

	// Tracing
	if tracingEnabled {
		app.Use(commonMiddleware.Tracing(commonMiddleware.TracingConfig{
			ServiceName: serviceName,
		}))
	}

	// Security headers
	app.Use(middleware.SecurityHeaders())

	// CORS
	if cfg.CORS.Enabled {
		app.Use(middleware.CORS(cfg.CORS.AllowedOrigins))
	}

	// Metrics
	app.Use(middleware.Metrics())

	// Structured access log middleware (uses go-common v0.1.2+)
	app.Use(commonMiddleware.AccessLog(commonMiddleware.LoadAccessLogConfigFromEnv("gateway-service", nil)))

	// Tenant extraction
	app.Use(middleware.TenantExtractor())

	// Auth middleware — strips identity headers globally.
	// Route-level enforcement happens in the router (via EnforcePolicy).
	if auth != nil {
		app.Use(auth.Handler())
	}

	// Rate limiting
	if rateLimiter != nil {
		app.Use(rateLimiter.Middleware())
	}
}

func gatewayErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
	}
	return respond.WriteError(c, code, "INTERNAL_ERROR", err.Error(), fiber.Map{
		"success": false,
		"error": fiber.Map{
			"code":    "INTERNAL_ERROR",
			"message": err.Error(),
		},
	})
}
