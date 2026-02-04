package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// RequestID adds a unique request ID to each request
func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Check for existing request ID
		requestID := c.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}

		// Set in response header
		c.Set("X-Request-ID", requestID)

		// Store in context for logging
		c.Locals("request_id", requestID)

		return c.Next()
	}
}

// SecurityHeaders adds security headers to responses
func SecurityHeaders() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Security headers
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("X-Frame-Options", "DENY")
		c.Set("X-XSS-Protection", "1; mode=block")
		c.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Set("Content-Security-Policy", "default-src 'self'")
		c.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		// Remove server information
		c.Set("Server", "")

		return c.Next()
	}
}

// CORS handles Cross-Origin Resource Sharing
func CORS(allowedOrigins []string) fiber.Handler {
	originsMap := make(map[string]bool)
	for _, origin := range allowedOrigins {
		originsMap[origin] = true
	}

	return func(c *fiber.Ctx) error {
		origin := c.Get("Origin")

		// Check if origin is allowed
		if origin != "" && (len(allowedOrigins) == 0 || originsMap[origin] || originsMap["*"]) {
			c.Set("Access-Control-Allow-Origin", origin)
			c.Set("Access-Control-Allow-Credentials", "true")
		}

		// Handle preflight requests
		if c.Method() == "OPTIONS" {
			c.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Set("Access-Control-Allow-Headers", "Origin, Content-Type, Accept, Authorization, X-Request-ID, X-Tenant-ID")
			c.Set("Access-Control-Max-Age", "86400") // 24 hours
			return c.SendStatus(fiber.StatusNoContent)
		}

		return c.Next()
	}
}

// ContentType validates and enforces content type
func ContentType() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// For POST/PUT/PATCH requests, validate content type
		method := c.Method()
		if method == "POST" || method == "PUT" || method == "PATCH" {
			contentType := c.Get("Content-Type")
			if contentType == "" && len(c.Body()) > 0 {
				return c.Status(fiber.StatusUnsupportedMediaType).JSON(fiber.Map{
					"error":   "unsupported_media_type",
					"message": "Content-Type header is required for request body",
				})
			}
		}

		return c.Next()
	}
}

// RequestTimeout adds timeout to requests
func RequestTimeout(handler fiber.Handler) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// The actual timeout is handled by Fiber's config
		// This middleware can add timeout context if needed
		return handler(c)
	}
}

// HeaderTransform transforms headers for upstream requests
type HeaderTransform struct {
	AddHeaders    map[string]string
	RemoveHeaders []string
	RenameHeaders map[string]string
}

// TransformHeaders middleware for header manipulation
func TransformHeaders(transform HeaderTransform) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Add headers
		for key, value := range transform.AddHeaders {
			c.Request().Header.Set(key, value)
		}

		// Remove headers
		for _, key := range transform.RemoveHeaders {
			c.Request().Header.Del(key)
		}

		// Rename headers
		for oldKey, newKey := range transform.RenameHeaders {
			if value := c.Get(oldKey); value != "" {
				c.Request().Header.Set(newKey, value)
				c.Request().Header.Del(oldKey)
			}
		}

		return c.Next()
	}
}

// Recover handles panics gracefully
func Recover() fiber.Handler {
	return func(c *fiber.Ctx) error {
		defer func() {
			if r := recover(); r != nil {
				// Log the panic
				requestID, _ := c.Locals("request_id").(string)
				_ = requestID // Use for logging

				c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
					"error":      "internal_server_error",
					"message":    "An unexpected error occurred",
					"request_id": requestID,
				})
			}
		}()

		return c.Next()
	}
}
