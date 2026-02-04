package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// RouteConfig defines routing rules
type RouteConfig struct {
	Routes []Route `yaml:"routes"`
}

// Route defines a single route mapping
type Route struct {
	Path           string       `yaml:"path"`
	Service        string       `yaml:"service"`
	StripPrefix    bool         `yaml:"stripPrefix"`
	Methods        []string     `yaml:"methods"`
	Public         bool         `yaml:"public"`
	RateLimit      *RouteLimit  `yaml:"rateLimit,omitempty"`
	Timeout        string       `yaml:"timeout,omitempty"`
	CircuitBreaker bool         `yaml:"circuitBreaker"`
	Retry          *RetryConfig `yaml:"retry,omitempty"`
	Cache          *CacheConfig `yaml:"cache,omitempty"`
}

// RouteLimit defines per-route rate limiting
type RouteLimit struct {
	RequestsPerSec int `yaml:"requestsPerSec"`
	BurstSize      int `yaml:"burstSize"`
}

// RetryConfig defines retry behavior
type RetryConfig struct {
	MaxAttempts int    `yaml:"maxAttempts"`
	WaitTime    string `yaml:"waitTime"`
}

// CacheConfig defines response caching
type CacheConfig struct {
	Enabled bool     `yaml:"enabled"`
	TTL     string   `yaml:"ttl"`
	Methods []string `yaml:"methods"`
}

// LoadRoutes loads route configuration from YAML file
func LoadRoutes(path string) (*RouteConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultRoutes(), nil
	}

	var config RouteConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// DefaultRoutes returns default routing configuration
func DefaultRoutes() *RouteConfig {
	return &RouteConfig{
		Routes: []Route{
			// Auth service routes
			{
				Path:           "/api/v1/auth",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"GET", "POST", "PUT", "DELETE", "PATCH"},
				Public:         false,
				CircuitBreaker: true,
			},
			{
				Path:           "/api/v1/auth/login",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"POST"},
				Public:         true,
				CircuitBreaker: true,
				RateLimit: &RouteLimit{
					RequestsPerSec: 10,
					BurstSize:      20,
				},
			},
			{
				Path:           "/api/v1/auth/register",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"POST"},
				Public:         true,
				CircuitBreaker: true,
				RateLimit: &RouteLimit{
					RequestsPerSec: 5,
					BurstSize:      10,
				},
			},
			{
				Path:           "/api/v1/auth/refresh",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"POST"},
				Public:         true,
				CircuitBreaker: true,
			},
			{
				Path:           "/api/v1/auth/verify-email",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"POST", "GET"},
				Public:         true,
				CircuitBreaker: true,
			},
			{
				Path:           "/api/v1/auth/forgot-password",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"POST"},
				Public:         true,
				CircuitBreaker: true,
				RateLimit: &RouteLimit{
					RequestsPerSec: 3,
					BurstSize:      5,
				},
			},
			{
				Path:           "/api/v1/auth/reset-password",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"POST"},
				Public:         true,
				CircuitBreaker: true,
			},
			// Users routes
			{
				Path:           "/api/v1/users",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"GET", "POST", "PUT", "DELETE", "PATCH"},
				Public:         false,
				CircuitBreaker: true,
			},
			// Roles routes
			{
				Path:           "/api/v1/roles",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"GET", "POST", "PUT", "DELETE"},
				Public:         false,
				CircuitBreaker: true,
			},
			// Permissions routes
			{
				Path:           "/api/v1/permissions",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"GET", "POST", "PUT", "DELETE"},
				Public:         false,
				CircuitBreaker: true,
			},
			// Admin routes
			{
				Path:           "/api/v1/admin",
				Service:        "auth",
				StripPrefix:    false,
				Methods:        []string{"GET", "POST", "PUT", "DELETE", "PATCH"},
				Public:         false,
				CircuitBreaker: true,
			},
			// Notifier service routes
			{
				Path:           "/api/v1/notifications",
				Service:        "notifier",
				StripPrefix:    false,
				Methods:        []string{"GET", "POST", "PUT", "DELETE"},
				Public:         false,
				CircuitBreaker: true,
			},
			{
				Path:           "/api/v1/templates",
				Service:        "notifier",
				StripPrefix:    false,
				Methods:        []string{"GET", "POST", "PUT", "DELETE"},
				Public:         false,
				CircuitBreaker: true,
			},
			{
				Path:           "/api/v1/preferences",
				Service:        "notifier",
				StripPrefix:    false,
				Methods:        []string{"GET", "POST", "PUT", "DELETE"},
				Public:         false,
				CircuitBreaker: true,
			},
			// Health endpoints (public)
			{
				Path:        "/health",
				Service:     "gateway",
				StripPrefix: false,
				Methods:     []string{"GET"},
				Public:      true,
			},
			{
				Path:        "/ready",
				Service:     "gateway",
				StripPrefix: false,
				Methods:     []string{"GET"},
				Public:      true,
			},
			// Metrics (internal)
			{
				Path:        "/metrics",
				Service:     "gateway",
				StripPrefix: false,
				Methods:     []string{"GET"},
				Public:      true,
			},
		},
	}
}
