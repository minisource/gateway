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
	"github.com/minisource/gateway/config"
	"github.com/minisource/gateway/internal/handler"
	"github.com/minisource/gateway/internal/middleware"
	"github.com/minisource/gateway/internal/proxy"
	"github.com/minisource/gateway/internal/router"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
)

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Load routes configuration
	routes, err := config.LoadRoutes("config/routes.yaml")
	if err != nil {
		log.Printf("Using default routes: %v", err)
		routes = config.DefaultRoutes()
	}

	// Initialize logger
	logger := middleware.NewLogger(cfg.Logging)
	logger.Info("Starting Minisource API Gateway")

	// Initialize tracer
	shutdownTracer, err := middleware.InitTracer(cfg.Tracing)
	if err != nil {
		log.Printf("Failed to initialize tracer: %v", err)
	}

	// Initialize Redis client (optional)
	var redisClient *redis.Client
	if cfg.Redis.Host != "" {
		redisClient = redis.NewClient(&redis.Options{
			Addr:     fmt.Sprintf("%s:%s", cfg.Redis.Host, cfg.Redis.Port),
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})

		// Test connection
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := redisClient.Ping(ctx).Err(); err != nil {
			logger.Warn("Redis connection failed, using in-memory rate limiter", "error", err)
			redisClient = nil
		} else {
			logger.Info("Connected to Redis")
		}
	}

	// Initialize service proxy
	serviceProxy := proxy.NewServiceProxy(&cfg.Services)
	serviceProxy.StartHealthChecks(30 * time.Second)

	// Initialize circuit breaker manager
	cbManager := middleware.NewCircuitBreakerManager(cfg.Circuit)

	// Initialize rate limiter
	rateLimiter := middleware.NewRateLimiter(cfg.RateLimit, redisClient)

	// Create Fiber app
	app := fiber.New(fiber.Config{
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
		ErrorHandler: middleware.ErrorLogger(logger),
		AppName:      "Minisource Gateway v1.0.0",
		// Disable default server header
		ServerHeader: "",
		// Enable trusted proxy
		EnableTrustedProxyCheck: true,
		TrustedProxies:          cfg.Server.TrustedProxies,
	})

	// Apply middleware stack (order matters!)
	setupMiddleware(app, cfg, routes, logger, cbManager, rateLimiter)

	// Setup health endpoints
	healthHandler := handler.NewHealthHandler(serviceProxy)
	healthHandler.RegisterRoutes(app)

	// Prometheus metrics endpoint
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	// Circuit breaker status endpoint
	app.Get("/circuit-breakers", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"states": cbManager.GetAllStates(),
		})
	})

	// Setup routes
	gatewayRouter := router.New(app, serviceProxy, routes, cfg)
	gatewayRouter.SetupRoutes()

	// Start server in goroutine
	go func() {
		addr := fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port)
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

	// Shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := app.ShutdownWithContext(ctx); err != nil {
		logger.Error("Server forced to shutdown", "error", err)
	}

	// Cleanup
	if err := serviceProxy.Close(); err != nil {
		logger.Error("Failed to close proxy", "error", err)
	}

	if redisClient != nil {
		if err := redisClient.Close(); err != nil {
			logger.Error("Failed to close Redis", "error", err)
		}
	}

	if shutdownTracer != nil {
		if err := shutdownTracer(ctx); err != nil {
			logger.Error("Failed to shutdown tracer", "error", err)
		}
	}

	logger.Info("Gateway stopped")
}

// setupMiddleware configures the middleware stack
func setupMiddleware(
	app *fiber.App,
	cfg *config.Config,
	routes *config.RouteConfig,
	logger *middleware.SimpleLogger,
	cbManager *middleware.CircuitBreakerManager,
	rateLimiter *middleware.RateLimiter,
) {
	// Recovery - must be first
	app.Use(recover.New(recover.Config{
		EnableStackTrace: true,
	}))

	// Request ID - early for tracing
	app.Use(middleware.RequestID())

	// Security headers
	app.Use(middleware.SecurityHeaders())

	// CORS
	app.Use(middleware.CORS([]string{"*"}))

	// Tracing
	if cfg.Tracing.Enabled {
		app.Use(middleware.Tracing(cfg.Tracing.ServiceName))
	}

	// Metrics
	app.Use(middleware.Metrics())

	// Request logging
	app.Use(middleware.RequestLogger(logger))

	// Content type validation
	app.Use(middleware.ContentType())

	// Tenant extraction
	app.Use(middleware.TenantExtractor())

	// Authentication (after public routes are set up)
	app.Use(middleware.NewAuthMiddleware(cfg, routes))

	// Rate limiting
	app.Use(rateLimiter.Middleware())

	// Circuit breaker
	app.Use(cbManager.Middleware())
}
