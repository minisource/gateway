package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/minisource/gateway/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Test Helpers ──────────────────────────────────────────

func generateTestKeyPair(t *testing.T) *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

func intToBytes(v int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(v))
	for len(b) > 1 && b[0] == 0 {
		b = b[1:]
	}
	return b
}

func setupTestJWKSServer(t *testing.T, key *rsa.PrivateKey) (string, func()) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		kid := "test-key-1"
		n := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(intToBytes(key.E))
		jwks := map[string]interface{}{
			"keys": []map[string]interface{}{
				{"kid": kid, "kty": "RSA", "alg": "RS256", "use": "sig", "n": n, "e": e},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	})
	server := httptest.NewServer(mux)
	return server.URL, func() { server.Close() }
}

func createRS256Token(t *testing.T, key *rsa.PrivateKey, kid string, claims *GatewayClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	tokenString, err := token.SignedString(key)
	require.NoError(t, err)
	return tokenString
}

func createHS256Token(t *testing.T, secret string, claims *GatewayClaims) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(secret))
	require.NoError(t, err)
	return tokenString
}

// setupTestApp creates a Fiber app that calls EnforcePolicy in the route handler.
// This mirrors the real gateway architecture (global middleware strips headers, route handler enforces policy).
func setupTestApp(auth *AuthMiddleware, policy string) *fiber.App {
	app := fiber.New()

	// Global middleware: strips identity headers
	if auth != nil {
		app.Use(auth.Handler())
	}

	// Route handler: enforces policy via EnforcePolicy (like the real router)
	app.All("/test", func(c *fiber.Ctx) error {
		if auth != nil {
			if err := auth.EnforcePolicy(c, policy); err != nil {
				return err
			}
		}
		return c.JSON(fiber.Map{
			"user_id": c.Locals("user_id"),
			"policy":  policy,
		})
	})

	app.All("/check-headers", func(c *fiber.Ctx) error {
		if auth != nil {
			if err := auth.EnforcePolicy(c, policy); err != nil {
				return err
			}
		}
		return c.JSON(fiber.Map{
			"x-user-id":    string(c.Request().Header.Peek("X-User-ID")),
			"x-user-email": string(c.Request().Header.Peek("X-User-Email")),
			"x-user-roles": string(c.Request().Header.Peek("X-User-Roles")),
		})
	})

	return app
}

// ─── Public Route Tests ───────────────────────────────────

func TestEnforcePolicy_Public_NoToken(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{Enabled: true, ValidateAtGateway: true})
	app := setupTestApp(auth, "public")

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestEnforcePolicy_Public_StripsIdentityHeaders(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{Enabled: true, ValidateAtGateway: true})
	app := setupTestApp(auth, "public")

	req := httptest.NewRequest("GET", "/check-headers", nil)
	req.Header.Set("X-User-ID", "attacker-id")
	req.Header.Set("X-User-Email", "attacker@evil.com")

	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.NotContains(t, bodyStr, "attacker-id", "incoming X-User-ID should be stripped by global middleware")
	assert.NotContains(t, bodyStr, "attacker@evil.com", "incoming X-User-Email should be stripped")
}

// ─── Authenticated Route Tests ──────────────────────────

func TestEnforcePolicy_Authenticated_MissingToken(t *testing.T) {
	key := generateTestKeyPair(t)
	jwksURL, cleanup := setupTestJWKSServer(t, key)
	defer cleanup()

	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "jwks",
		ValidateAtGateway: true,
		JWKSURL:           jwksURL,
		Issuer:            "minisource-auth",
	})
	time.Sleep(50 * time.Millisecond) // wait for JWKS fetch
	app := setupTestApp(auth, "authenticated")

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

func TestEnforcePolicy_Authenticated_InvalidToken(t *testing.T) {
	key := generateTestKeyPair(t)
	jwksURL, cleanup := setupTestJWKSServer(t, key)
	defer cleanup()

	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "jwks",
		ValidateAtGateway: true,
		JWKSURL:           jwksURL,
		Issuer:            "minisource-auth",
	})
	app := setupTestApp(auth, "authenticated")

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid-token-here")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

func TestEnforcePolicy_Authenticated_ValidToken_SetsIdentityHeaders(t *testing.T) {
	// Use HMAC mode for this test — identity header behavior is identical regardless of auth mode.
	// JWKS-specific validation is covered by the expired/wrong issuer tests.
	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "hmac",
		ValidateAtGateway: true,
		Secret:            "test-secret-hmac",
		Issuer:            "minisource-auth",
		Audience:          "minisource",
	})

	app := setupTestApp(auth, "authenticated")

	claims := &GatewayClaims{
		UserID: "user-123",
		Email:  "user@example.com",
		Roles:  []string{"user"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			Issuer:    "minisource-auth",
			Audience:  jwt.ClaimStrings{"minisource"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := createHS256Token(t, "test-secret-hmac", claims)

	req := httptest.NewRequest("GET", "/check-headers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-User-ID", "attacker-id")

	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "user-123", "trusted X-User-ID should be set")
	assert.Contains(t, bodyStr, "user@example.com", "trusted X-User-Email should be set")
	assert.NotContains(t, bodyStr, "attacker-id", "attacker X-User-ID should be stripped")
}

func TestEnforcePolicy_Authenticated_ExpiredToken(t *testing.T) {
	key := generateTestKeyPair(t)
	jwksURL, cleanup := setupTestJWKSServer(t, key)
	defer cleanup()

	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "jwks",
		ValidateAtGateway: true,
		JWKSURL:           jwksURL,
		Issuer:            "minisource-auth",
	})
	app := setupTestApp(auth, "authenticated")

	claims := &GatewayClaims{
		UserID: "user-123",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			Issuer:    "minisource-auth",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
	}
	token := createRS256Token(t, key, "test-key-1", claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

func TestEnforcePolicy_Authenticated_WrongIssuer(t *testing.T) {
	key := generateTestKeyPair(t)
	jwksURL, cleanup := setupTestJWKSServer(t, key)
	defer cleanup()

	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "jwks",
		ValidateAtGateway: true,
		JWKSURL:           jwksURL,
		Issuer:            "minisource-auth",
	})
	app := setupTestApp(auth, "authenticated")

	claims := &GatewayClaims{
		UserID: "user-123",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			Issuer:    "wrong-issuer",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := createRS256Token(t, key, "test-key-1", claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

// ─── HMAC Mode Tests ─────────────────────────────────────

func TestEnforcePolicy_HMACMode_ValidToken(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "hmac",
		ValidateAtGateway: true,
		Secret:            "my-hmac-secret",
	})
	app := setupTestApp(auth, "authenticated")

	claims := &GatewayClaims{
		UserID: "user-hmac",
		Email:  "hmac@example.com",
		Roles:  []string{"user"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-hmac",
			Issuer:    "minisource-auth",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := createHS256Token(t, "my-hmac-secret", claims)

	req := httptest.NewRequest("GET", "/check-headers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-User-ID", "attacker")

	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	assert.Contains(t, bodyStr, "user-hmac", "trusted HMAC user ID should be set")
	assert.NotContains(t, bodyStr, "attacker", "attacker headers should be stripped")
}

// ─── No Auto Fallback Tests ──────────────────────────────

func TestEnforcePolicy_NoAutoFallback_JWKSNotToHMAC(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "jwks",
		ValidateAtGateway: true,
		Secret:            "my-secret",
		JWKSURL:           "", // empty URL means no JWKS validator
	})
	app := setupTestApp(auth, "authenticated")

	claims := &GatewayClaims{
		UserID: "user-test",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-test",
			Issuer:    "minisource-auth",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := createHS256Token(t, "my-secret", claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode, "JWKS mode should NOT fall back to HMAC")
}

func TestEnforcePolicy_HMACMode_NotToJWKS(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "hmac",
		ValidateAtGateway: true,
		Secret:            "hmac-only-secret",
		JWKSURL:           "http://localhost/jwks.json",
	})
	app := setupTestApp(auth, "authenticated")

	key := generateTestKeyPair(t)
	claims := &GatewayClaims{
		UserID: "user-rs256",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-rs256",
			Issuer:    "minisource-auth",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := createRS256Token(t, key, "test-key-1", claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode, "HMAC mode should NOT accept RS256 tokens")
}

// ─── Admin Route Tests ──────────────────────────────────

func TestEnforcePolicy_Admin_NoAdminRole(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "hmac",
		ValidateAtGateway: true,
		Secret:            "admin-secret",
	})
	app := setupTestApp(auth, "admin")

	claims := &GatewayClaims{
		UserID: "normal-user",
		Roles:  []string{"user"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "normal-user",
			Issuer:    "minisource-auth",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := createHS256Token(t, "admin-secret", claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 403, resp.StatusCode, "non-admin should get 403 on admin route")
}

func TestEnforcePolicy_Admin_WithAdminRole(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "hmac",
		ValidateAtGateway: true,
		Secret:            "admin-secret",
	})
	app := setupTestApp(auth, "admin")

	claims := &GatewayClaims{
		UserID: "admin-user",
		Roles:  []string{"admin"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "admin-user",
			Issuer:    "minisource-auth",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := createHS256Token(t, "admin-secret", claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "admin should pass admin route")
}

// ─── Disabled/Internal Route Tests ────────────────────

func TestEnforcePolicy_Disabled(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{Enabled: true})
	app := setupTestApp(auth, "disabled")

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode, "disabled routes should return 404")
}

func TestEnforcePolicy_Internal(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{Enabled: true})
	app := setupTestApp(auth, "internal")

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 404, resp.StatusCode, "internal routes should return 404")
}

// ─── Auth Disabled Tests ──────────────────────────────

func TestEnforcePolicy_AuthDisabled_PassesThrough(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled: false,
	})
	app := setupTestApp(auth, "authenticated")

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "disabled auth should pass through")
}

func TestEnforcePolicy_ValidateDisabled_PassesThrough(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		ValidateAtGateway: false,
	})
	app := setupTestApp(auth, "authenticated")

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode, "disabled validation should pass through")
}

// ─── Skip Path Tests ───────────────────────────────────

func TestGlobalMiddleware_SkipPaths(t *testing.T) {
	auth := NewAuthMiddleware(&config.AuthCfg{
		Enabled:           true,
		Mode:              "hmac",
		ValidateAtGateway: true,
		Secret:            "test-secret",
	})

	app := fiber.New()
	app.Use(auth.Handler())
	app.Get("/health", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/ready", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/metrics", func(c *fiber.Ctx) error { return c.SendString("ok") })
	app.Get("/swagger/index.html", func(c *fiber.Ctx) error { return c.SendString("ok") })

	for _, path := range []string{"/health", "/ready", "/metrics", "/swagger/index.html"} {
		req := httptest.NewRequest("GET", path, nil)
		resp, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, 200, resp.StatusCode, "skip path %s should be 200", path)
	}
}

// ─── Legacy Compatibility Tests ──────────────────────

func TestLegacyAuth_PublicPaths(t *testing.T) {
	cfg := DefaultAuthConfig("test-secret")
	cfg.PublicPaths["/v1/auth/login"] = []string{"POST"}

	app := fiber.New()
	app.Use(Auth(cfg))
	app.Post("/v1/auth/login", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("POST", "/v1/auth/login", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestLegacyAuth_MissingToken(t *testing.T) {
	cfg := DefaultAuthConfig("test-secret")

	app := fiber.New()
	app.Use(Auth(cfg))
	app.Get("/protected", func(c *fiber.Ctx) error { return c.SendString("ok") })

	req := httptest.NewRequest("GET", "/protected", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 401, resp.StatusCode)
}

func TestLegacyAuth_ValidToken_SetsIdentityHeaders(t *testing.T) {
	cfg := DefaultAuthConfig("test-secret")

	app := fiber.New()
	app.Use(Auth(cfg))
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"x-user-id":    string(c.Request().Header.Peek("X-User-ID")),
			"x-user-email": string(c.Request().Header.Peek("X-User-Email")),
		})
	})

	claims := &GatewayClaims{
		UserID: "legacy-user",
		Email:  "legacy@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "legacy-user",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := createHS256Token(t, "test-secret", claims)

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "legacy-user")
	assert.Contains(t, string(body), "legacy@example.com")
}
