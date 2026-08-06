// Deprecated: Legacy route types kept for backward compatibility only.
// These types are still referenced by middleware (circuit.go, ratelimit.go, auth.go legacy bridge).
//
// The gateway now uses config/gateway_config.go (GatewayConfig) for all configuration.
// Routes are defined in gateway_config.go :: HostCfg.Routes[] using the RouteCfg type.
// See routes.example.yaml for the canonical route definitions.
//
// TODO: Migrate middleware references to the new RouteCfg type, then delete this file.
package config

// RouteConfig defines routing rules (legacy — use GatewayConfig.Hosts instead).
type RouteConfig struct {
	Routes []Route `yaml:"routes"`
}

// Route defines a single route mapping (legacy — use RouteCfg instead).
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

// RouteLimit defines per-route rate limiting (legacy).
type RouteLimit struct {
	RequestsPerSec int `yaml:"requestsPerSec"`
	BurstSize      int `yaml:"burstSize"`
}

// RetryConfig defines retry behavior (legacy).
type RetryConfig struct {
	MaxAttempts int    `yaml:"maxAttempts"`
	WaitTime    string `yaml:"waitTime"`
}

// CacheConfig defines response caching (legacy).
type CacheConfig struct {
	Enabled bool     `yaml:"enabled"`
	TTL     string   `yaml:"ttl"`
	Methods []string `yaml:"methods"`
}
