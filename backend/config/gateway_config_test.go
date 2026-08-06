package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultGatewayConfig(t *testing.T) {
	cfg := defaultGatewayConfig()

	assert.Equal(t, "9000", cfg.Server.Port)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, "development", cfg.App.Env)
	assert.Equal(t, "debug", cfg.App.LogLevel)
	assert.True(t, cfg.Routing.AllowDevHostFallback, "dev host fallback should default to true in dev")

	// Notifier must be internal only
	svc, ok := cfg.Services["notifier"]
	require.True(t, ok)
	assert.True(t, svc.InternalOnly)
	assert.False(t, svc.Public)

	// Auth should be in services
	_, ok = cfg.Services["auth"]
	assert.True(t, ok)

	// Private projects (YadYar, DiviPay) should NOT be in default public config
	_, ok = cfg.Services["yadyar"]
	assert.False(t, ok, "private project YadYar must not be in public default config")
	_, ok = cfg.Services["divipay"]
	assert.False(t, ok, "private project DiviPay must not be in public default config")

	// Localhost host config should have granular auth routes
	require.Len(t, cfg.Hosts, 1)
	assert.Equal(t, "localhost", cfg.Hosts[0].Host)
	assert.True(t, len(cfg.Hosts[0].Routes) >= 6, "should have granular auth routes")

	// Verify granular auth routes exist
	routeIDs := make(map[string]bool)
	for _, r := range cfg.Hosts[0].Routes {
		routeIDs[r.ID] = true
	}
	assert.True(t, routeIDs["auth-login"], "auth-login route should exist")
	assert.True(t, routeIDs["auth-register"], "auth-register route should exist")
	assert.True(t, routeIDs["auth-refresh"], "auth-refresh route should exist")
	assert.True(t, routeIDs["auth-userinfo"], "auth-userinfo route should exist")
	assert.True(t, routeIDs["auth-logout"], "auth-logout route should exist")
	assert.True(t, routeIDs["jwks"], "jwks route should exist")
}

func TestApplyDefaults(t *testing.T) {
	cfg := &GatewayConfig{
		App: AppCfg{},
		Auth: AuthCfg{
			Enabled: true,
			JWKSURL: "http://localhost/jwks.json",
		},
		Services: ServicesMap{
			"notifier": {InternalOnly: true},
			"test":     {},
		},
	}
	applyDefaults(cfg)

	assert.Equal(t, "9000", cfg.Server.Port)
	assert.Equal(t, "0.0.0.0", cfg.Server.Host)
	assert.Equal(t, 15*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, "info", cfg.App.LogLevel)
	assert.Equal(t, "jwks", cfg.Auth.Mode, "auto-detect jwks mode from jwksUrl")
	assert.False(t, cfg.Services["notifier"].Public, "notifier should be forced private")
	assert.Equal(t, 30*time.Second, cfg.Services["test"].Timeout)
	assert.Equal(t, "/health", cfg.Services["test"].HealthPath)
}

func TestApplyDefaults_HMACMode(t *testing.T) {
	cfg := &GatewayConfig{
		App: AppCfg{},
		Auth: AuthCfg{
			Enabled: true,
			Secret:  "mysecret",
		},
		Services: ServicesMap{},
	}
	applyDefaults(cfg)
	assert.Equal(t, "hmac", cfg.Auth.Mode, "should auto-detect hmac mode from secret when no jwks")
}

func TestValidateProduction_SafeConfig(t *testing.T) {
	cfg := &GatewayConfig{
		App: AppCfg{Env: "production"},
		Routing: RoutingCfg{
			AllowDevHostFallback: false,
		},
		CORS: CORSCfg{
			Enabled:          true,
			AllowedOrigins:   []string{"https://app.example.com"},
			AllowCredentials: true,
		},
		Auth: AuthCfg{
			Enabled:           true,
			Mode:              "jwks",
			ValidateAtGateway: true,
			JWKSURL:           "https://auth.example.com/jwks.json",
		},
		Services: ServicesMap{
			"notifier": {InternalOnly: true, Public: false},
		},
	}
	err := cfg.ValidateProduction()
	assert.NoError(t, err)
}

func TestValidateProduction_UnsafeDevHostFallback(t *testing.T) {
	cfg := &GatewayConfig{
		App: AppCfg{Env: "production"},
		Routing: RoutingCfg{
			AllowDevHostFallback: true, // UNSAFE in prod
		},
		Auth: AuthCfg{
			Mode: "jwks",
		},
	}
	err := cfg.ValidateProduction()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowDevHostFallback")
}

func TestValidateProduction_UnsafeCORS(t *testing.T) {
	cfg := &GatewayConfig{
		App: AppCfg{Env: "production"},
		Routing: RoutingCfg{
			AllowDevHostFallback: false,
		},
		CORS: CORSCfg{
			Enabled:          true,
			AllowedOrigins:   []string{"*"}, // UNSAFE wildcard with credentials
			AllowCredentials: true,
		},
		Auth: AuthCfg{
			Mode: "jwks",
		},
	}
	err := cfg.ValidateProduction()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "allowedOrigins")
}

func TestValidateProduction_NotifierPublic(t *testing.T) {
	cfg := &GatewayConfig{
		App: AppCfg{Env: "staging"},
		Routing: RoutingCfg{
			AllowDevHostFallback: false,
		},
		Auth: AuthCfg{
			Mode: "jwks",
		},
		Services: ServicesMap{
			"notifier": {Public: true, InternalOnly: false}, // UNSAFE
		},
	}
	err := cfg.ValidateProduction()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "notifier")
}

func TestValidateProduction_NoAuthMode(t *testing.T) {
	cfg := &GatewayConfig{
		App: AppCfg{Env: "production"},
		Routing: RoutingCfg{
			AllowDevHostFallback: false,
		},
		Auth: AuthCfg{
			Enabled:           true,
			ValidateAtGateway: true,
			Mode:              "", // missing mode
			Secret:            "secret",
			AllowLegacyHMAC:   false,
		},
	}
	err := cfg.ValidateProduction()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mode")
}

func TestValidateProduction_DevModeIgnoresValidation(t *testing.T) {
	cfg := &GatewayConfig{
		App: AppCfg{Env: "development"},
		Routing: RoutingCfg{
			AllowDevHostFallback: true, // okay in dev
		},
		CORS: CORSCfg{
			AllowedOrigins:   []string{"*"},
			AllowCredentials: true,
		},
	}
	err := cfg.ValidateProduction()
	assert.NoError(t, err, "dev mode should skip production validation")
}

func TestLoadGatewayConfig_FileNotFound(t *testing.T) {
	cfg, err := LoadGatewayConfig("nonexistent-file.yaml")
	require.NoError(t, err, "missing config file should return defaults, not error")
	assert.Equal(t, "9000", cfg.Server.Port)
	assert.Equal(t, "development", cfg.App.Env)
}

func TestLoadGatewayConfig_EnvOverrides(t *testing.T) {
	os.Setenv("GATEWAY_PORT", "9999")
	os.Setenv("GATEWAY_LOG_LEVEL", "warn")
	defer os.Unsetenv("GATEWAY_PORT")
	defer os.Unsetenv("GATEWAY_LOG_LEVEL")

	cfg, err := LoadGatewayConfig("nonexistent-file.yaml")
	require.NoError(t, err)
	assert.Equal(t, "9999", cfg.Server.Port)
	assert.Equal(t, "warn", cfg.App.LogLevel)
}

func TestLoadGatewayConfig_YAMLParsing(t *testing.T) {
	// Write a temporary YAML config
	yamlContent := `
server:
  port: 8080
  host: "127.0.0.1"
app:
  env: "staging"
  logLevel: "info"
routing:
  allowDevHostFallback: false
auth:
  enabled: true
  mode: "jwks"
  jwksUrl: "http://localhost/jwks.json"
  validateAtGateway: true
services:
  auth:
    baseUrl: "http://localhost:9001"
hosts:
  - host: "api.test.com"
    routes:
      - id: "test-route"
        pathPrefix: "/v1/test"
        upstream: "auth"
        policy: "authenticated"
        methods: ["GET"]
`
	tmpFile, err := os.CreateTemp("", "gateway-config-test-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())
	_, err = tmpFile.WriteString(yamlContent)
	require.NoError(t, err)
	tmpFile.Close()

	cfg, err := LoadGatewayConfig(tmpFile.Name())
	require.NoError(t, err)
	assert.Equal(t, "8080", cfg.Server.Port)
	assert.Equal(t, "staging", cfg.App.Env)
	assert.False(t, cfg.Routing.AllowDevHostFallback)
	assert.True(t, cfg.Auth.Enabled)
	assert.Equal(t, "jwks", cfg.Auth.Mode)
	assert.True(t, cfg.Auth.ValidateAtGateway)

	require.Len(t, cfg.Hosts, 1)
	assert.Equal(t, "api.test.com", cfg.Hosts[0].Host)
	require.Len(t, cfg.Hosts[0].Routes, 1)
	assert.Equal(t, "test-route", cfg.Hosts[0].Routes[0].ID)
	assert.Equal(t, "/v1/test", cfg.Hosts[0].Routes[0].PathPrefix)
	assert.Equal(t, "authenticated", cfg.Hosts[0].Routes[0].Policy)
}

func TestLoadGatewayConfigFiles_MultiFileMerge(t *testing.T) {
	baseYAML := `
services:
  auth:
    baseUrl: "http://localhost:9001"
hosts:
  - host: "localhost"
    routes:
      - id: "auth-login"
        pathPrefix: "/v1/auth/login"
        upstream: "auth"
        policy: "public"
        methods: ["POST"]
`
	overlayYAML := `
services:
  custom-private-service:
    baseUrl: "http://localhost:9999"
hosts:
  - host: "localhost"
    routes:
      - id: "custom-route"
        pathPrefix: "/v1/custom"
        upstream: "custom-private-service"
        policy: "authenticated"
        methods: ["GET"]
`
	f1, err := os.CreateTemp("", "base-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(f1.Name())
	f1.WriteString(baseYAML)
	f1.Close()

	f2, err := os.CreateTemp("", "overlay-config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(f2.Name())
	f2.WriteString(overlayYAML)
	f2.Close()

	cfg, err := LoadGatewayConfigFiles(f1.Name(), f2.Name())
	require.NoError(t, err)

	// Base service retained
	_, hasAuth := cfg.Services["auth"]
	assert.True(t, hasAuth, "base auth service should be retained")

	// Overlay service merged
	svc, hasCustom := cfg.Services["custom-private-service"]
	assert.True(t, hasCustom, "overlay private service should be merged")
	assert.Equal(t, "http://localhost:9999", svc.BaseURL)

	// Routes merged under localhost host
	require.Len(t, cfg.Hosts, 1)
	assert.Equal(t, "localhost", cfg.Hosts[0].Host)

	routeMap := make(map[string]RouteCfg)
	for _, r := range cfg.Hosts[0].Routes {
		routeMap[r.ID] = r
	}
	assert.Contains(t, routeMap, "auth-login")
	assert.Contains(t, routeMap, "custom-route")
}
