package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Server    ServerConfig
	Services  ServicesConfig
	Redis     RedisConfig
	JWT       JWTConfig
	RateLimit RateLimitConfig
	Circuit   CircuitConfig
	Tracing   TracingConfig
	Logging   LoggingConfig
}

type ServerConfig struct {
	Port            string
	Host            string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	TrustedProxies  []string
}

type ServicesConfig struct {
	Auth     ServiceConfig
	Notifier ServiceConfig
}

type ServiceConfig struct {
	URL             string
	Timeout         time.Duration
	MaxIdleConns    int
	MaxConnsPerHost int
	HealthPath      string
}

type RedisConfig struct {
	Host     string
	Port     string
	Password string
	DB       int
}

type JWTConfig struct {
	Secret           string
	AccessExpiresIn  time.Duration
	RefreshExpiresIn time.Duration
}

type RateLimitConfig struct {
	Enabled         bool
	RequestsPerSec  int
	BurstSize       int
	CleanupInterval time.Duration
}

type CircuitConfig struct {
	Enabled          bool
	MaxRequests      uint32
	Interval         time.Duration
	Timeout          time.Duration
	FailureThreshold uint32
}

type TracingConfig struct {
	Enabled     bool
	ServiceName string
	Endpoint    string
	SampleRate  float64
}

type LoggingConfig struct {
	Level  string
	Format string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	return &Config{
		Server: ServerConfig{
			Port:            getEnv("SERVER_PORT", "8080"),
			Host:            getEnv("SERVER_HOST", "0.0.0.0"),
			ReadTimeout:     getDuration("SERVER_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:    getDuration("SERVER_WRITE_TIMEOUT", 30*time.Second),
			IdleTimeout:     getDuration("SERVER_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout: getDuration("SERVER_SHUTDOWN_TIMEOUT", 30*time.Second),
			TrustedProxies:  getEnvSlice("TRUSTED_PROXIES", []string{"127.0.0.1"}),
		},
		Services: ServicesConfig{
			Auth: ServiceConfig{
				URL:             getEnv("AUTH_SERVICE_URL", "http://localhost:5000"),
				Timeout:         getDuration("AUTH_SERVICE_TIMEOUT", 30*time.Second),
				MaxIdleConns:    getEnvInt("AUTH_MAX_IDLE_CONNS", 100),
				MaxConnsPerHost: getEnvInt("AUTH_MAX_CONNS_PER_HOST", 100),
				HealthPath:      getEnv("AUTH_HEALTH_PATH", "/api/health"),
			},
			Notifier: ServiceConfig{
				URL:             getEnv("NOTIFIER_SERVICE_URL", "http://localhost:5001"),
				Timeout:         getDuration("NOTIFIER_SERVICE_TIMEOUT", 30*time.Second),
				MaxIdleConns:    getEnvInt("NOTIFIER_MAX_IDLE_CONNS", 100),
				MaxConnsPerHost: getEnvInt("NOTIFIER_MAX_CONNS_PER_HOST", 100),
				HealthPath:      getEnv("NOTIFIER_HEALTH_PATH", "/api/health"),
			},
		},
		Redis: RedisConfig{
			Host:     getEnv("REDIS_HOST", "localhost"),
			Port:     getEnv("REDIS_PORT", "6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvInt("REDIS_DB", 0),
		},
		JWT: JWTConfig{
			Secret:           getEnv("JWT_SECRET", "your-secret-key"),
			AccessExpiresIn:  getDuration("JWT_ACCESS_EXPIRES", 15*time.Minute),
			RefreshExpiresIn: getDuration("JWT_REFRESH_EXPIRES", 7*24*time.Hour),
		},
		RateLimit: RateLimitConfig{
			Enabled:         getEnvBool("RATE_LIMIT_ENABLED", true),
			RequestsPerSec:  getEnvInt("RATE_LIMIT_RPS", 100),
			BurstSize:       getEnvInt("RATE_LIMIT_BURST", 200),
			CleanupInterval: getDuration("RATE_LIMIT_CLEANUP", 1*time.Minute),
		},
		Circuit: CircuitConfig{
			Enabled:          getEnvBool("CIRCUIT_ENABLED", true),
			MaxRequests:      uint32(getEnvInt("CIRCUIT_MAX_REQUESTS", 5)),
			Interval:         getDuration("CIRCUIT_INTERVAL", 60*time.Second),
			Timeout:          getDuration("CIRCUIT_TIMEOUT", 30*time.Second),
			FailureThreshold: uint32(getEnvInt("CIRCUIT_FAILURE_THRESHOLD", 5)),
		},
		Tracing: TracingConfig{
			Enabled:     getEnvBool("TRACING_ENABLED", true),
			ServiceName: getEnv("SERVICE_NAME", "minisource-gateway"),
			Endpoint:    getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318"),
			SampleRate:  getEnvFloat("TRACING_SAMPLE_RATE", 1.0),
		},
		Logging: LoggingConfig{
			Level:  getEnv("LOG_LEVEL", "info"),
			Format: getEnv("LOG_FORMAT", "json"),
		},
	}, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

func getEnvFloat(key string, defaultValue float64) float64 {
	if value := os.Getenv(key); value != "" {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}

func getDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return defaultValue
}

func getEnvSlice(key string, defaultValue []string) []string {
	if value := os.Getenv(key); value != "" {
		return strings.Split(value, ",")
	}
	return defaultValue
}
