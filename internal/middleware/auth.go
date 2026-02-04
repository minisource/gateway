package middleware

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/minisource/gateway/config"
)

// AuthConfig holds authentication middleware configuration
type AuthConfig struct {
	JWTSecret    string
	PublicPaths  map[string][]string // path -> methods
	HeaderName   string
	TokenPrefix  string
	ContextKey   string
	SkipPrefixes []string
}

// DefaultAuthConfig returns default auth configuration
func DefaultAuthConfig(secret string) AuthConfig {
	return AuthConfig{
		JWTSecret:    secret,
		PublicPaths:  make(map[string][]string),
		HeaderName:   "Authorization",
		TokenPrefix:  "Bearer ",
		ContextKey:   "user",
		SkipPrefixes: []string{"/health", "/ready", "/live", "/metrics"},
	}
}

// Claims represents JWT claims
type Claims struct {
	UserID   string   `json:"user_id"`
	TenantID string   `json:"tenant_id"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	jwt.RegisteredClaims
}

// Auth creates JWT authentication middleware
func Auth(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		path := c.Path()
		method := c.Method()

		// Skip prefixes (health, metrics, etc.)
		for _, prefix := range cfg.SkipPrefixes {
			if strings.HasPrefix(path, prefix) {
				return c.Next()
			}
		}

		// Check if route is marked as public
		if isPublic, ok := c.Locals("isPublic").(bool); ok && isPublic {
			return c.Next()
		}

		// Check public paths
		if methods, ok := cfg.PublicPaths[path]; ok {
			for _, m := range methods {
				if strings.EqualFold(m, method) {
					return c.Next()
				}
			}
		}

		// Get token from header
		authHeader := c.Get(cfg.HeaderName)
		if authHeader == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error":   "unauthorized",
				"message": "Missing authorization header",
			})
		}

		// Extract token
		tokenString := strings.TrimPrefix(authHeader, cfg.TokenPrefix)
		if tokenString == authHeader {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error":   "unauthorized",
				"message": "Invalid authorization format",
			})
		}

		// Parse and validate token
		claims, err := validateToken(tokenString, cfg.JWTSecret)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error":   "unauthorized",
				"message": err.Error(),
			})
		}

		// Store claims in context
		c.Locals(cfg.ContextKey, claims)
		c.Locals("user_id", claims.UserID)
		c.Locals("tenant_id", claims.TenantID)

		// Add user info to headers for downstream services
		c.Request().Header.Set("X-User-ID", claims.UserID)
		c.Request().Header.Set("X-Tenant-ID", claims.TenantID)
		c.Request().Header.Set("X-User-Email", claims.Email)
		if len(claims.Roles) > 0 {
			c.Request().Header.Set("X-User-Roles", strings.Join(claims.Roles, ","))
		}

		return c.Next()
	}
}

// validateToken validates JWT token and returns claims
func validateToken(tokenString, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fiber.NewError(fiber.StatusUnauthorized, "Invalid signing method")
		}
		return []byte(secret), nil
	})

	if err != nil {
		return nil, fiber.NewError(fiber.StatusUnauthorized, "Invalid token")
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fiber.NewError(fiber.StatusUnauthorized, "Invalid token claims")
	}

	// Check expiration
	if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
		return nil, fiber.NewError(fiber.StatusUnauthorized, "Token expired")
	}

	return claims, nil
}

// RequireRoles middleware checks if user has required roles
func RequireRoles(roles ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals("user").(*Claims)
		if !ok {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error":   "unauthorized",
				"message": "No user context found",
			})
		}

		for _, requiredRole := range roles {
			for _, userRole := range claims.Roles {
				if strings.EqualFold(requiredRole, userRole) {
					return c.Next()
				}
			}
		}

		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
			"error":   "forbidden",
			"message": "Insufficient permissions",
		})
	}
}

// TenantExtractor extracts tenant ID from various sources
func TenantExtractor() fiber.Handler {
	return func(c *fiber.Ctx) error {
		var tenantID string

		// 1. From JWT claims (already set by Auth middleware)
		if tid := c.Locals("tenant_id"); tid != nil {
			tenantID = tid.(string)
		}

		// 2. From X-Tenant-ID header (for service-to-service calls)
		if tenantID == "" {
			tenantID = c.Get("X-Tenant-ID")
		}

		// 3. From subdomain (e.g., tenant1.example.com)
		if tenantID == "" {
			host := c.Hostname()
			parts := strings.Split(host, ".")
			if len(parts) >= 3 {
				tenantID = parts[0]
			}
		}

		if tenantID != "" {
			c.Locals("tenant_id", tenantID)
			c.Request().Header.Set("X-Tenant-ID", tenantID)
		}

		return c.Next()
	}
}

// NewAuthMiddleware creates auth middleware from config
func NewAuthMiddleware(cfg *config.Config, routes *config.RouteConfig) fiber.Handler {
	authCfg := DefaultAuthConfig(cfg.JWT.Secret)

	// Build public paths from routes
	for _, route := range routes.Routes {
		if route.Public {
			authCfg.PublicPaths[route.Path] = route.Methods
		}
	}

	return Auth(authCfg)
}
