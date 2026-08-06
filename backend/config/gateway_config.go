package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ─── Gateway Config (unified YAML + env overrides) ──────────

// GatewayConfig is the top-level config for the gateway (loaded from YAML).
type GatewayConfig struct {
	Server    ServerCfg    `yaml:"server"`
	App       AppCfg       `yaml:"app"`
	CORS      CORSCfg      `yaml:"cors"`
	Auth      AuthCfg      `yaml:"auth"`
	RateLimit RateLimitCfg `yaml:"rateLimit"`
	Routing   RoutingCfg   `yaml:"routing"`
	Services  ServicesMap  `yaml:"services"`
	Hosts     []HostCfg    `yaml:"hosts"`
}

type RoutingCfg struct {
	AllowDevHostFallback bool `yaml:"allowDevHostFallback"` // Only in development. Production must be false.
}

type ServerCfg struct {
	Host            string        `yaml:"host"`
	Port            string        `yaml:"port"`
	ReadTimeout     time.Duration `yaml:"readTimeout"`
	WriteTimeout    time.Duration `yaml:"writeTimeout"`
	IdleTimeout     time.Duration `yaml:"idleTimeout"`
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout"`
	TrustedProxies  []string      `yaml:"trustedProxies"`
	MaxBodyBytes    int           `yaml:"maxBodyBytes"`
}

type AppCfg struct {
	Name     string `yaml:"name"`
	Env      string `yaml:"env"`
	LogLevel string `yaml:"logLevel"`
	LogFmt   string `yaml:"logFormat"`
}

type CORSCfg struct {
	Enabled          bool     `yaml:"enabled"`
	AllowedOrigins   []string `yaml:"allowedOrigins"`
	AllowedMethods   []string `yaml:"allowedMethods"`
	AllowedHeaders   []string `yaml:"allowedHeaders"`
	ExposedHeaders   []string `yaml:"exposedHeaders"`
	AllowCredentials bool     `yaml:"allowCredentials"`
}

type AuthCfg struct {
	Enabled           bool   `yaml:"enabled"`
	Mode              string `yaml:"mode"` // jwks | hmac | disabled
	JWKSURL           string `yaml:"jwksUrl"`
	Issuer            string `yaml:"issuer"`
	Audience          string `yaml:"audience"`
	ValidateAtGateway bool   `yaml:"validateAtGateway"`
	Secret            string `yaml:"secret"`          // HS256 secret, only used if mode=hmac
	AllowLegacyHMAC   bool   `yaml:"allowLegacyHMAC"` // Require explicit opt-in for HMAC fallback in production
}

type RateLimitCfg struct {
	Enabled           bool          `yaml:"enabled"`
	RequestsPerMinute int           `yaml:"requestsPerMinute"`
	Burst             int           `yaml:"burst"`
	CleanupInterval   time.Duration `yaml:"cleanupInterval"` // Local limiter cleanup interval
}

// ServicesMap maps service name → config (dynamic via YAML keys).
type ServicesMap map[string]*ServiceCfg

type ServiceCfg struct {
	BaseURL      string        `yaml:"baseUrl"`
	Timeout      time.Duration `yaml:"timeout"`
	HealthPath   string        `yaml:"healthPath"`
	Public       bool          `yaml:"public"`
	InternalOnly bool          `yaml:"internalOnly"`
}

// ─── Host / Route Config ────────────────────────────────────

type HostCfg struct {
	Host   string     `yaml:"host"`
	Routes []RouteCfg `yaml:"routes"`
}

type RouteCfg struct {
	ID                 string   `yaml:"id"`
	PathPrefix         string   `yaml:"pathPrefix"`
	Upstream           string   `yaml:"upstream"`
	StripPrefix        string   `yaml:"stripPrefix"`
	UpstreamPathPrefix string   `yaml:"upstreamPathPrefix"`
	Policy             string   `yaml:"policy"` // public | authenticated | admin | internal | disabled
	Methods            []string `yaml:"methods"`
}

// ─── Loading ─────────────────────────────────────────────────

// ─── Loading ─────────────────────────────────────────────────

// LoadGatewayConfig loads config from primary YAML file and any local overlays or GATEWAY_CONFIG_FILES.
func LoadGatewayConfig(path string) (*GatewayConfig, error) {
	var paths []string

	if envFiles := os.Getenv("GATEWAY_CONFIG_FILES"); envFiles != "" {
		for _, p := range strings.Split(envFiles, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				paths = append(paths, p)
			}
		}
	} else if path != "" {
		paths = append(paths, path)
		// Check for auto-detected local overlays if not explicitly specified via env
		localCandidates := []string{
			"configs.local/services.local.yaml",
			"config.local.yaml",
			strings.TrimSuffix(path, ".yaml") + ".local.yaml",
		}
		for _, candidate := range localCandidates {
			if _, err := os.Stat(candidate); err == nil {
				paths = append(paths, candidate)
			}
		}
	}

	if len(paths) == 0 {
		cfg := defaultGatewayConfig()
		applyEnvOverrides(cfg)
		applyDefaults(cfg)
		return cfg, nil
	}

	return LoadGatewayConfigFiles(paths...)
}

// LoadGatewayConfigFiles loads multiple configuration files in sequence and merges them.
func LoadGatewayConfigFiles(paths ...string) (*GatewayConfig, error) {
	cfg := defaultGatewayConfig()
	loadedCount := 0

	for i, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) && i > 0 {
				// Secondary/overlay file missing is ignored unless explicitly required
				continue
			}
			if os.IsNotExist(err) && i == 0 {
				applyEnvOverrides(cfg)
				applyDefaults(cfg)
				return cfg, nil
			}
			return nil, fmt.Errorf("read config file %s: %w", path, err)
		}

		overlay := &GatewayConfig{}
		if err := yaml.Unmarshal(data, overlay); err != nil {
			return nil, fmt.Errorf("parse config file %s: %w", path, err)
		}

		if loadedCount == 0 {
			cfg = overlay
		} else {
			MergeGatewayConfig(cfg, overlay)
		}
		loadedCount++
	}

	applyEnvOverrides(cfg)
	applyDefaults(cfg)
	return cfg, nil
}

// MergeGatewayConfig merges overlay into base. Overlay services and routes take precedence or append.
func MergeGatewayConfig(base, overlay *GatewayConfig) {
	if overlay == nil {
		return
	}

	// Merge Services
	if overlay.Services != nil {
		if base.Services == nil {
			base.Services = make(ServicesMap)
		}
		for name, svc := range overlay.Services {
			base.Services[name] = svc
		}
	}

	// Merge Hosts
	if overlay.Hosts != nil {
		for _, oHost := range overlay.Hosts {
			matched := false
			for idx := range base.Hosts {
				if strings.EqualFold(base.Hosts[idx].Host, oHost.Host) {
					matched = true
					// Merge routes into existing host
					for _, oRoute := range oHost.Routes {
						rMatched := false
						for rIdx := range base.Hosts[idx].Routes {
							if (oRoute.ID != "" && base.Hosts[idx].Routes[rIdx].ID == oRoute.ID) ||
								(base.Hosts[idx].Routes[rIdx].PathPrefix == oRoute.PathPrefix) {
								base.Hosts[idx].Routes[rIdx] = oRoute
								rMatched = true
								break
							}
						}
						if !rMatched {
							base.Hosts[idx].Routes = append(base.Hosts[idx].Routes, oRoute)
						}
					}
					break
				}
			}
			if !matched {
				base.Hosts = append(base.Hosts, oHost)
			}
		}
	}

	// Merge top-level settings if set in overlay
	if overlay.Server.Port != "" {
		base.Server.Port = overlay.Server.Port
	}
	if overlay.App.Name != "" {
		base.App.Name = overlay.App.Name
	}
	if overlay.App.Env != "" {
		base.App.Env = overlay.App.Env
	}
}

func applyEnvOverrides(cfg *GatewayConfig) {
	if v := os.Getenv("GATEWAY_PORT"); v != "" {
		cfg.Server.Port = v
	}
	if v := os.Getenv("GATEWAY_LOG_LEVEL"); v != "" {
		cfg.App.LogLevel = v
	}
	if v := os.Getenv("AUTH_SERVICE_URL"); v != "" {
		if cfg.Services["auth"] != nil {
			cfg.Services["auth"].BaseURL = v
		}
	}
	if v := os.Getenv("NOTIFIER_SERVICE_URL"); v != "" {
		if cfg.Services["notifier"] != nil {
			cfg.Services["notifier"].BaseURL = v
		}
	}
	if v := os.Getenv("AUTH_FRONTEND_URL"); v != "" {
		if cfg.Services["auth-frontend"] != nil {
			cfg.Services["auth-frontend"].BaseURL = v
		}
	}
	if v := os.Getenv("NOTIFIER_FRONTEND_URL"); v != "" {
		if cfg.Services["notifier-frontend"] != nil {
			cfg.Services["notifier-frontend"].BaseURL = v
		}
	}
}

func defaultGatewayConfig() *GatewayConfig {
	return &GatewayConfig{
		Server: ServerCfg{
			Host:            "0.0.0.0",
			Port:            "9000",
			ReadTimeout:     15 * time.Second,
			WriteTimeout:    30 * time.Second,
			IdleTimeout:     60 * time.Second,
			ShutdownTimeout: 30 * time.Second,
			TrustedProxies:  []string{"127.0.0.1"},
			MaxBodyBytes:    10 * 1024 * 1024,
		},
		App: AppCfg{
			Name:     "minisource-gateway",
			Env:      "development",
			LogLevel: "debug",
			LogFmt:   "console",
		},
		CORS: CORSCfg{
			Enabled:        true,
			AllowedOrigins: []string{"*"},
			AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders: []string{
				"Authorization", "Content-Type", "Accept", "Origin",
				"X-Request-ID", "X-Correlation-Id",
				"X-Tenant-Id", "X-Application-Code",
				"X-Language", "Accept-Language",
				"Idempotency-Key",
				"Content-Length", "Range", "If-None-Match",
			},
			ExposedHeaders: []string{
				"X-Request-ID", "X-Correlation-Id",
				"Content-Type", "Content-Length", "Content-Disposition",
				"ETag", "Cache-Control", "Accept-Ranges",
			},
		},
		Auth: AuthCfg{
			Enabled:           false,
			ValidateAtGateway: true,
		},
		RateLimit: RateLimitCfg{
			Enabled:           false,
			RequestsPerMinute: 120,
			Burst:             60,
			CleanupInterval:   5 * time.Minute,
		},
		Routing: RoutingCfg{
			AllowDevHostFallback: true, // dev default
		},
		Services: ServicesMap{
			"auth":      {BaseURL: "http://127.0.0.1:9001", Timeout: 30 * time.Second, HealthPath: "/health", Public: true},
			"notifier":  {BaseURL: "http://127.0.0.1:9002", Timeout: 30 * time.Second, HealthPath: "/health", InternalOnly: true},
			"log":       {BaseURL: "http://127.0.0.1:5002", Timeout: 30 * time.Second, HealthPath: "/health"},
			"scheduler": {BaseURL: "http://127.0.0.1:5003", Timeout: 30 * time.Second, HealthPath: "/health"},
			"storage":   {BaseURL: "http://127.0.0.1:5004", Timeout: 60 * time.Second, HealthPath: "/health"},
			"comment":   {BaseURL: "http://127.0.0.1:5010", Timeout: 30 * time.Second, HealthPath: "/health"},
			"ticket":    {BaseURL: "http://127.0.0.1:5011", Timeout: 30 * time.Second, HealthPath: "/health"},
			"feedback":  {BaseURL: "http://127.0.0.1:5012", Timeout: 30 * time.Second, HealthPath: "/health"},
			"payment":   {BaseURL: "http://127.0.0.1:5005", Timeout: 30 * time.Second, HealthPath: "/health"},
		},
		Hosts: []HostCfg{
			{
				Host: "localhost",
				Routes: []RouteCfg{
					// Auth — granular routes by policy
					{ID: "auth-login", PathPrefix: "/v1/auth/login", Upstream: "auth", UpstreamPathPrefix: "/v1/auth/login", Policy: "public", Methods: []string{"POST"}},
					{ID: "auth-register", PathPrefix: "/v1/auth/register", Upstream: "auth", UpstreamPathPrefix: "/v1/auth/register", Policy: "public", Methods: []string{"POST"}},
					{ID: "auth-refresh", PathPrefix: "/v1/auth/refresh", Upstream: "auth", UpstreamPathPrefix: "/v1/auth/refresh", Policy: "public", Methods: []string{"POST"}},
					{ID: "auth-google", PathPrefix: "/v1/auth/google", Upstream: "auth", UpstreamPathPrefix: "/v1/auth/google", Policy: "public", Methods: []string{"GET", "POST"}},
					{ID: "auth-otp-send", PathPrefix: "/v1/auth/otp/send", Upstream: "auth", UpstreamPathPrefix: "/v1/auth/otp/send", Policy: "public", Methods: []string{"POST"}},
					{ID: "auth-otp-verify", PathPrefix: "/v1/auth/otp/verify", Upstream: "auth", UpstreamPathPrefix: "/v1/auth/otp/verify", Policy: "public", Methods: []string{"POST"}},
					{ID: "jwks", PathPrefix: "/.well-known", Upstream: "auth", UpstreamPathPrefix: "/.well-known", Policy: "public", Methods: []string{"GET"}},
					{ID: "auth-userinfo", PathPrefix: "/v1/auth/userinfo", Upstream: "auth", UpstreamPathPrefix: "/v1/auth/userinfo", Policy: "authenticated", Methods: []string{"GET"}},
					{ID: "auth-logout", PathPrefix: "/v1/auth/logout", Upstream: "auth", UpstreamPathPrefix: "/v1/auth/logout", Policy: "authenticated", Methods: []string{"POST"}},
					// Users — profile, sessions (protected)
					{ID: "auth-users", PathPrefix: "/v1/users", Upstream: "auth", UpstreamPathPrefix: "/v1/users", Policy: "authenticated", Methods: []string{"GET", "PUT", "POST", "DELETE"}},
					// Auth catch-all for other paths
					{ID: "auth-other", PathPrefix: "/v1/auth", Upstream: "auth", UpstreamPathPrefix: "/v1/auth", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
					// Storage — Gateway /v1/storage/* → upstream /api/v1/*
					{ID: "storage", PathPrefix: "/v1/storage", Upstream: "storage", StripPrefix: "/v1/storage", UpstreamPathPrefix: "/api/v1", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
					// Public share access (no auth)
					{ID: "storage-share-public", PathPrefix: "/share", Upstream: "storage", UpstreamPathPrefix: "/share", Policy: "public", Methods: []string{"GET"}},
					// Payment
					{ID: "payment", PathPrefix: "/v1/payment", Upstream: "payment", StripPrefix: "/v1/payment", UpstreamPathPrefix: "/v1", Policy: "authenticated", Methods: []string{"GET", "POST"}},
					// Comment
					{ID: "comment", PathPrefix: "/v1/comment", Upstream: "comment", StripPrefix: "/v1/comment", UpstreamPathPrefix: "/v1", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "DELETE"}},
					// Ticket
					{ID: "ticket", PathPrefix: "/v1/ticket", Upstream: "ticket", StripPrefix: "/v1/ticket", UpstreamPathPrefix: "/v1", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
					// Feedback
					{ID: "feedback", PathPrefix: "/v1/feedback", Upstream: "feedback", StripPrefix: "/v1/feedback", UpstreamPathPrefix: "/v1", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "DELETE"}},
					// Notifier
					{ID: "notifier-notifications", PathPrefix: "/v1/notifications", Upstream: "notifier", UpstreamPathPrefix: "/v1/notifications", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
					{ID: "notifier-templates", PathPrefix: "/v1/templates", Upstream: "notifier", UpstreamPathPrefix: "/v1/templates", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
					{ID: "notifier-preferences", PathPrefix: "/v1/preferences", Upstream: "notifier", UpstreamPathPrefix: "/v1/preferences", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
					{ID: "notifier-reminders", PathPrefix: "/v1/reminders", Upstream: "notifier", UpstreamPathPrefix: "/v1/reminders", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
					{ID: "notifier-deliveries", PathPrefix: "/v1/deliveries", Upstream: "notifier", UpstreamPathPrefix: "/v1/deliveries", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
					{ID: "notifier-providers", PathPrefix: "/v1/providers", Upstream: "notifier", UpstreamPathPrefix: "/v1/providers", Policy: "admin", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
					{ID: "notifier-catchall", PathPrefix: "/v1/notifier", Upstream: "notifier", UpstreamPathPrefix: "/v1", Policy: "authenticated", Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}},
				},
			},
		},
	}
}

func applyDefaults(cfg *GatewayConfig) {
	if cfg.Server.Port == "" {
		cfg.Server.Port = "9000"
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 15 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 30 * time.Second
	}
	if cfg.Server.IdleTimeout == 0 {
		cfg.Server.IdleTimeout = 60 * time.Second
	}
	if cfg.Server.ShutdownTimeout == 0 {
		cfg.Server.ShutdownTimeout = 30 * time.Second
	}
	if cfg.App.LogLevel == "" {
		cfg.App.LogLevel = "info"
	}
	if cfg.App.LogFmt == "" {
		cfg.App.LogFmt = "console"
	}
	// Default auth mode: if jwksUrl is set, use jwks; else if secret set, use hmac; else disabled
	if cfg.Auth.Mode == "" {
		if cfg.Auth.JWKSURL != "" {
			cfg.Auth.Mode = "jwks"
		} else if cfg.Auth.Secret != "" {
			cfg.Auth.Mode = "hmac"
		}
	}
	if cfg.RateLimit.CleanupInterval == 0 {
		cfg.RateLimit.CleanupInterval = 5 * time.Minute
	}
	// Default notifier to internal only
	if svc, ok := cfg.Services["notifier"]; ok && svc.InternalOnly {
		svc.Public = false
	}
	for _, svc := range cfg.Services {
		if svc.Timeout == 0 {
			svc.Timeout = 30 * time.Second
		}
		if svc.HealthPath == "" {
			svc.HealthPath = "/health"
		}
	}
}

// ValidateProduction checks for unsafe config in production/staging.
// Returns nil if config is production-safe, or an error describing what must change.
func (cfg *GatewayConfig) ValidateProduction() error {
	isProd := cfg.App.Env == "production" || cfg.App.Env == "staging"
	if !isProd {
		return nil
	}

	var issues []string

	// 1. Dev host fallback must be disabled in production
	if cfg.Routing.AllowDevHostFallback {
		issues = append(issues, "routing.allowDevHostFallback must be false in production/staging")
	}

	// 2. CORS: wildcard origin with credentials is unsafe
	if cfg.CORS.Enabled && cfg.CORS.AllowCredentials {
		for _, origin := range cfg.CORS.AllowedOrigins {
			if origin == "*" {
				issues = append(issues, "cors.allowedOrigins must not contain '*' with allowCredentials=true in production")
				break
			}
		}
	}

	// 3. Auth: HMAC fallback must be explicit in production
	if cfg.Auth.Enabled && cfg.Auth.Mode == "" && cfg.Auth.Secret != "" && !cfg.Auth.AllowLegacyHMAC {
		issues = append(issues, "auth.allowLegacyHMAC must be true to enable HMAC fallback in production")
	}

	// 4. Auth: Mode should be explicit
	if cfg.Auth.Enabled && cfg.Auth.ValidateAtGateway && cfg.Auth.Mode == "" {
		issues = append(issues, "auth.mode must be set to 'jwks' or 'hmac' when validateAtGateway=true in production")
	}

	// 5. Notifier must not be publicly exposed
	if svc, ok := cfg.Services["notifier"]; ok && svc != nil && svc.Public && !svc.InternalOnly {
		issues = append(issues, "notifier service must not be public in production (set internalOnly: true)")
	}

	// 6. Swagger proxy routes must be disabled in production
	for _, host := range cfg.Hosts {
		for i := range host.Routes {
			route := &host.Routes[i]
			if strings.Contains(strings.ToLower(route.ID), "swagger") && route.Policy != "disabled" {
				// Auto-disable swagger routes in production instead of failing hard
				route.Policy = "disabled"
			}
		}
	}

	if len(issues) > 0 {
		return fmt.Errorf("production safety validation failed:\n  - %s", strings.Join(issues, "\n  - "))
	}
	return nil
}
