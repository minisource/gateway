package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/minisource/gateway/internal/respond"
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
		c.Set("X-Frame-Options", "SAMEORIGIN")
		c.Set("X-XSS-Protection", "1; mode=block")
		c.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

		// Content Security Policy supporting Next.js SSR, hydration scripts, inline styles & fonts
		c.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline' 'unsafe-eval'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"style-src-elem 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' data: https://fonts.gstatic.com; "+
				"img-src 'self' data: blob: https:; "+
				"connect-src 'self' http: https: ws: wss:;")

		// Remove server information
		c.Set("Server", "")

		return c.Next()
	}
}

// CORS handles Cross-Origin Resource Sharing
func CORS(allowedOrigins []string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		origin := c.Get("Origin")

		if origin != "" {
			c.Set("Access-Control-Allow-Origin", origin)
			c.Set("Access-Control-Allow-Credentials", "true")
		} else {
			c.Set("Access-Control-Allow-Origin", "*")
		}

		// Handle preflight requests
		if c.Method() == "OPTIONS" {
			c.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Set("Access-Control-Allow-Headers", "*")
			c.Set("Access-Control-Expose-Headers", "*")
			c.Set("Access-Control-Max-Age", "86400")
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
					"message": respond.T(c, "errors.content_type_required"),
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
					"message":    respond.T(c, "errors.unexpected_error"),
					"request_id": requestID,
				})
			}
		}()

		return c.Next()
	}
}
