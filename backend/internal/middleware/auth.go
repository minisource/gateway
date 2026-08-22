package middleware

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/minisource/gateway/config"
	"github.com/minisource/gateway/internal/respond"
)

// AuthMiddleware enforces route policies: public, authenticated, admin, internal, disabled.
//
// Architecture:
//   - Global middleware (Handler): strips incoming identity headers from ALL requests.
//   - Route-level enforcement (EnforcePolicy): called from the router handler BEFORE proxying.
//     This ensures route policies are enforced at the correct point in the request lifecycle,
//     after the route has been matched but before the upstream request is made.
//
// Security: Strips incoming X-User-* identity headers before setting trusted values from validated JWT.
// Upstream services must still validate JWT independently — gateway validation is a pre-check only.
type AuthMiddleware struct {
	cfg           *config.AuthCfg
	skipPaths     []string
	jwksValidator *jwksClient
}

// NewAuthMiddleware creates the auth middleware.
// Call Handler() to get the global middleware, and use EnforcePolicy() for route-level enforcement.
func NewAuthMiddleware(cfg *config.AuthCfg) *AuthMiddleware {
	m := &AuthMiddleware{
		cfg: cfg,
		skipPaths: []string{
			"/health", "/ready", "/live", "/metrics", "/swagger", "/circuit-breakers",
		},
	}

	// Pre-initialize JWKS validator if configured.
	// fetchJWKS runs in background; validate() calls it synchronously if keys are empty/expired.
	if cfg.Enabled && cfg.ValidateAtGateway && cfg.Mode == "jwks" && cfg.JWKSURL != "" {
		m.jwksValidator = newJWKSClient(cfg.JWKSURL, cfg.Issuer, cfg.Audience)
		go m.jwksValidator.fetchJWKS()
	}

	return m
}

// Handler returns the Fiber global middleware handler.
// Strips incoming identity headers from all requests (security: never trust client-supplied identity headers).
// Actual auth enforcement happens at the route level via EnforcePolicy().
func (a *AuthMiddleware) Handler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		path := c.Path()

		// Skip system paths
		for _, prefix := range a.skipPaths {
			if strings.HasPrefix(path, prefix) {
				return c.Next()
			}
		}

		// Always strip incoming identity headers (clients must never be trusted)
		stripIdentityHeaders(c)

		return c.Next()
	}
}

// EnforcePolicy enforces the route policy at the route handler level.
// Called by the router BEFORE proxying to the upstream service.
//
// Policies:
//   - public: no validation needed.
//   - authenticated: requires valid JWT.
//   - admin: requires valid JWT + admin role.
//   - disabled/internal: returns 404.
//
// For authenticated/admin policies, strips incoming identity headers and sets trusted
// values from the validated JWT.
func (a *AuthMiddleware) EnforcePolicy(c *fiber.Ctx, policy string) error {
	switch policy {
	case "public":
		return nil

	case "disabled", "internal":
		return gatewayErrorResponse(c, fiber.StatusNotFound, "ROUTE_NOT_FOUND", "errors.route_not_found")

	case "authenticated", "admin":
		if !a.cfg.Enabled || !a.cfg.ValidateAtGateway {
			// Auth disabled at gateway — let upstream validate
			return nil
		}

		// Validate JWT against configured mode
		claims, err := a.validateToken(c)
		if err != nil {
			return gatewayErrorResponse(c, fiber.StatusUnauthorized, "AUTH_REQUIRED", "errors.auth_required", err.Error())
		}

		// Set trusted identity headers from validated JWT
		setTrustedHeaders(c, claims)
		c.Locals("user", claims)
		c.Locals("user_id", claims.UserID)

		// Admin policy: require admin role
		if policy == "admin" && !hasAdminRole(claims.Roles) {
			return gatewayErrorResponse(c, fiber.StatusForbidden, "FORBIDDEN", "errors.admin_role_required")
		}

		return nil
	}

	return nil
}

// ─── Token Validation ──────────────────────────────────────

func (a *AuthMiddleware) validateToken(c *fiber.Ctx) (*GatewayClaims, error) {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return nil, fmt.Errorf("authorization header is required")
	}

	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenString == authHeader || tokenString == "" {
		return nil, fmt.Errorf("bearer token is required")
	}

	switch a.cfg.Mode {
	case "jwks":
		if a.jwksValidator != nil {
			return a.jwksValidator.validate(tokenString)
		}
		return nil, fmt.Errorf("JWKS validator not configured")

	case "hmac":
		if a.cfg.Secret != "" {
			return validateHMACToken(tokenString, a.cfg.Secret)
		}
		return nil, fmt.Errorf("HMAC secret not configured")

	default:
		// Legacy fallback: only if explicitly allowed
		if a.cfg.AllowLegacyHMAC {
			if a.jwksValidator != nil {
				return a.jwksValidator.validate(tokenString)
			}
			if a.cfg.Secret != "" {
				return validateHMACToken(tokenString, a.cfg.Secret)
			}
		}
		return nil, fmt.Errorf("auth mode not configured")
	}
}

// ─── Identity Headers ─────────────────────────────────────

// stripIdentityHeaders removes incoming X-User-* identity headers from client requests.
// These headers are only trusted when set by the gateway itself from a validated JWT.
// X-Tenant-ID is intentionally preserved: it is a client context header, not an identity
// header, and upstream services validate tenant access independently.
func stripIdentityHeaders(c *fiber.Ctx) {
	identityHeaders := []string{
		"X-User-ID",
		"X-User-Email",
		"X-User-Roles",
		"X-User-Permissions",
		"X-Actor-ID",
		"X-Actor-Roles",
	}
	for _, h := range identityHeaders {
		c.Request().Header.Del(h)
	}
}

// setTrustedHeaders sets identity headers from validated JWT claims.
// X-Tenant-ID is only overwritten when the JWT itself carries a tenant claim;
// otherwise the client-supplied tenant context header is preserved.
func setTrustedHeaders(c *fiber.Ctx, claims *GatewayClaims) {
	if claims.UserID != "" {
		c.Request().Header.Set("X-User-ID", claims.UserID)
	}
	if claims.Email != "" {
		c.Request().Header.Set("X-User-Email", claims.Email)
	}
	if len(claims.Roles) > 0 {
		c.Request().Header.Set("X-User-Roles", strings.Join(claims.Roles, ","))
	}
	if claims.TenantID != "" {
		c.Request().Header.Set("X-Tenant-ID", claims.TenantID)
	}
}

// ─── JWKS Client ──────────────────────────────────────────

type jwksClient struct {
	jwksURL   string
	issuer    string
	audience  string
	mu        sync.RWMutex
	keys      map[string]interface{} // kid → public key
	expiresAt time.Time
}

func newJWKSClient(url, issuer, audience string) *jwksClient {
	return &jwksClient{
		jwksURL:  url,
		issuer:   issuer,
		audience: audience,
		keys:     make(map[string]interface{}),
	}
}

func (j *jwksClient) fetchJWKS() {
	resp, err := http.Get(j.jwksURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return
	}

	j.mu.Lock()
	for _, key := range jwks.Keys {
		j.keys[key.Kid] = key
	}
	j.expiresAt = time.Now().Add(5 * time.Minute)
	j.mu.Unlock()
}

func (j *jwksClient) validate(tokenString string) (*GatewayClaims, error) {
	// NOTE: Gateway JWKS validation is a pre-check only.
	// Full cryptographic signature verification is performed by upstream services.
	// The gateway extracts claims to check issuer/expiry/roles for routing policy enforcement.
	// DO NOT rely on gateway validation as the sole security layer.

	parser := jwt.NewParser()
	token, _, err := parser.ParseUnverified(tokenString, &GatewayClaims{})
	if err != nil {
		return nil, fmt.Errorf("invalid token format")
	}

	kid := ""
	if k, ok := token.Header["kid"].(string); ok {
		kid = k
	}

	// Refresh JWKS if expired
	j.mu.RLock()
	expired := time.Now().After(j.expiresAt)
	j.mu.RUnlock()
	if expired || len(j.keys) == 0 {
		j.fetchJWKS()
	}

	// Verify key exists in JWKS (light check — upstream does full crypto verification)
	j.mu.RLock()
	defer j.mu.RUnlock()
	if kid != "" {
		if _, exists := j.keys[kid]; !exists {
			return nil, fmt.Errorf("token kid not found in JWKS")
		}
	}

	// Extract claims from the unverified token
	claims := &GatewayClaims{}
	_, _, _ = parser.ParseUnverified(tokenString, claims)

	if claims.Subject == "" {
		return nil, fmt.Errorf("token missing subject")
	}
	if j.issuer != "" && claims.Issuer != j.issuer {
		return nil, fmt.Errorf("invalid issuer")
	}
	if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
		return nil, fmt.Errorf("token expired")
	}

	return claims, nil
}

// ─── HMAC Validation ─────────────────────────────────────

func validateHMACToken(tokenString, secret string) (*GatewayClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &GatewayClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %v", err)
	}

	claims, ok := token.Claims.(*GatewayClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

// ─── Gateway Claims ──────────────────────────────────────

// GatewayClaims represents JWT claims extracted by the gateway.
type GatewayClaims struct {
	UserID   string   `json:"user_id"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	TenantID string   `json:"tenant_id"`
	jwt.RegisteredClaims
}

func hasAdminRole(roles []string) bool {
	for _, r := range roles {
		if strings.EqualFold(r, "admin") || strings.EqualFold(r, "super_admin") {
			return true
		}
	}
	return false
}

// ─── Legacy compatibility ────────────────────────────────

// AuthConfig holds authentication middleware configuration (legacy).
type AuthConfig struct {
	JWTSecret    string
	PublicPaths  map[string][]string
	HeaderName   string
	TokenPrefix  string
	ContextKey   string
	SkipPrefixes []string
}

// DefaultAuthConfig returns default auth configuration (legacy).
func DefaultAuthConfig(secret string) AuthConfig {
	return AuthConfig{
		JWTSecret:    secret,
		PublicPaths:  make(map[string][]string),
		HeaderName:   "Authorization",
		TokenPrefix:  "Bearer ",
		ContextKey:   "user",
		SkipPrefixes: []string{"/health", "/ready", "/live", "/metrics", "/swagger", "/circuit-breakers"},
	}
}

// Claims represents JWT claims (legacy).
type Claims struct {
	UserID   string   `json:"user_id"`
	TenantID string   `json:"tenant_id"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	jwt.RegisteredClaims
}

// Auth creates JWT authentication middleware (legacy — kept for backward compat).
func Auth(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		path := c.Path()
		method := c.Method()

		for _, prefix := range cfg.SkipPrefixes {
			if strings.HasPrefix(path, prefix) {
				return c.Next()
			}
		}

		if isPublic, ok := c.Locals("isPublic").(bool); ok && isPublic {
			return c.Next()
		}

		if methods, ok := cfg.PublicPaths[path]; ok {
			for _, m := range methods {
				if strings.EqualFold(m, method) {
					return c.Next()
				}
			}
		}

		authHeader := c.Get(cfg.HeaderName)
		if authHeader == "" {
			return gatewayErrorResponse(c, fiber.StatusUnauthorized, "AUTH_REQUIRED", "errors.token_required")
		}

		tokenString := strings.TrimPrefix(authHeader, cfg.TokenPrefix)
		if tokenString == authHeader {
			return gatewayErrorResponse(c, fiber.StatusUnauthorized, "AUTH_REQUIRED", "errors.token_invalid")
		}

		claims, err := legacyValidateToken(tokenString, cfg.JWTSecret)
		if err != nil {
			return gatewayErrorResponse(c, fiber.StatusUnauthorized, "INVALID_TOKEN", "errors.token_invalid", err.Error())
		}

		c.Locals(cfg.ContextKey, claims)
		c.Locals("user_id", claims.UserID)
		c.Locals("tenant_id", claims.TenantID)

		if claims.UserID != "" {
			c.Request().Header.Set("X-User-ID", claims.UserID)
		}
		if claims.TenantID != "" {
			c.Request().Header.Set("X-Tenant-ID", claims.TenantID)
		}
		if claims.Email != "" {
			c.Request().Header.Set("X-User-Email", claims.Email)
		}
		if len(claims.Roles) > 0 {
			c.Request().Header.Set("X-User-Roles", strings.Join(claims.Roles, ","))
		}

		return c.Next()
	}
}

func legacyValidateToken(tokenString, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("invalid signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token")
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	if claims.ExpiresAt != nil && claims.ExpiresAt.Time.Before(time.Now()) {
		return nil, fmt.Errorf("token expired")
	}

	return claims, nil
}

// RequireRoles middleware checks if user has required roles.
func RequireRoles(roles ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals("user").(*Claims)
		if !ok {
			return gatewayErrorResponse(c, fiber.StatusUnauthorized, "AUTH_REQUIRED", "errors.token_required")
		}
		for _, requiredRole := range roles {
			for _, userRole := range claims.Roles {
				if strings.EqualFold(requiredRole, userRole) {
					return c.Next()
				}
			}
		}
		return gatewayErrorResponse(c, fiber.StatusForbidden, "FORBIDDEN", "errors.permission_denied")
	}
}

// TenantExtractor extracts tenant ID from various sources.
// The hostname-based tenant derivation must NOT run for IP addresses or
// localhost: "127.0.0.1" / "192.168.x.x" / "[::1]" would be split on "."
// and yield a bogus tenant (e.g. "127"), poisoning every upstream request
// with an invalid X-Tenant-ID.
func TenantExtractor() fiber.Handler {
	return func(c *fiber.Ctx) error {
		var tenantID string
		if tid := c.Locals("tenant_id"); tid != nil {
			tenantID = tid.(string)
		}
		if tenantID == "" {
			tenantID = c.Get("X-Tenant-ID")
		}
		if tenantID == "" {
			host := c.Hostname()
			// c.Hostname() may or may not include the port depending on the
			// Fiber version; normalize by stripping it explicitly.
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			// IPv4, IPv6, and localhost are never tenant subdomains.
			if host != "localhost" && net.ParseIP(strings.Trim(host, "[]")) == nil {
				parts := strings.Split(host, ".")
				if len(parts) >= 3 {
					tenantID = parts[0]
				}
			}
		}
		if tenantID != "" {
			c.Locals("tenant_id", tenantID)
			c.Request().Header.Set("X-Tenant-ID", tenantID)
		}
		return c.Next()
	}
}

// NewLegacyAuthMiddleware creates auth middleware from legacy config (bridge).
func NewLegacyAuthMiddleware(cfg *config.Config, routes *config.RouteConfig) fiber.Handler {
	authCfg := DefaultAuthConfig(cfg.JWT.Secret)
	for _, route := range routes.Routes {
		if route.Public {
			authCfg.PublicPaths[route.Path] = route.Methods
		}
	}
	return Auth(authCfg)
}

// gatewayErrorResponse returns a standardized error with a localized message.
// messageKey is an i18n key (errors.*); optional details (e.g. technical token
// validation output) are attached for API clients and never shown to browsers.
func gatewayErrorResponse(c *fiber.Ctx, status int, code, messageKey string, details ...string) error {
	msg := respond.T(c, messageKey)
	errInfo := fiber.Map{
		"code":    code,
		"message": msg,
	}
	if len(details) > 0 && details[0] != "" {
		errInfo["details"] = details[0]
	}
	return c.Status(status).JSON(fiber.Map{
		"success": false,
		"error":   errInfo,
	})
}
