package middleware

import (
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/config"
	"github.com/sony/gobreaker"
)

// CircuitBreakerManager manages circuit breakers for services
type CircuitBreakerManager struct {
	breakers map[string]*gobreaker.CircuitBreaker
	mu       sync.RWMutex
	cfg      config.CircuitConfig
}

// NewCircuitBreakerManager creates a new circuit breaker manager
func NewCircuitBreakerManager(cfg config.CircuitConfig) *CircuitBreakerManager {
	return &CircuitBreakerManager{
		breakers: make(map[string]*gobreaker.CircuitBreaker),
		cfg:      cfg,
	}
}

// GetBreaker returns or creates a circuit breaker for a service
func (m *CircuitBreakerManager) GetBreaker(serviceName string) *gobreaker.CircuitBreaker {
	m.mu.RLock()
	cb, exists := m.breakers[serviceName]
	m.mu.RUnlock()

	if exists {
		return cb
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if cb, exists = m.breakers[serviceName]; exists {
		return cb
	}

	// Create new circuit breaker
	cb = gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        serviceName,
		MaxRequests: m.cfg.MaxRequests,
		Interval:    m.cfg.Interval,
		Timeout:     m.cfg.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return counts.Requests >= uint32(m.cfg.FailureThreshold) && failureRatio >= 0.5
		},
		OnStateChange: func(name string, from gobreaker.State, to gobreaker.State) {
			// Log state changes (integrate with your logging)
			// This is where you'd send metrics about circuit state changes
		},
	})

	m.breakers[serviceName] = cb
	return cb
}

// GetState returns the current state of a circuit breaker
func (m *CircuitBreakerManager) GetState(serviceName string) gobreaker.State {
	m.mu.RLock()
	cb, exists := m.breakers[serviceName]
	m.mu.RUnlock()

	if !exists {
		return gobreaker.StateClosed
	}
	return cb.State()
}

// GetAllStates returns states of all circuit breakers
func (m *CircuitBreakerManager) GetAllStates() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	states := make(map[string]string)
	for name, cb := range m.breakers {
		states[name] = cb.State().String()
	}
	return states
}

// CircuitBreaker middleware wraps requests with circuit breaker
func (m *CircuitBreakerManager) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !m.cfg.Enabled {
			return c.Next()
		}

		// Get service name from context (set by router)
		serviceName, ok := c.Locals("service").(string)
		if !ok || serviceName == "" || serviceName == "gateway" {
			return c.Next()
		}

		// Check route config for circuit breaker flag
		if route, ok := c.Locals("route").(config.Route); ok {
			if !route.CircuitBreaker {
				return c.Next()
			}
		}

		cb := m.GetBreaker(serviceName)

		// Execute with circuit breaker
		result, err := cb.Execute(func() (interface{}, error) {
			// Store original response writer state
			err := c.Next()

			// Check if response indicates failure
			statusCode := c.Response().StatusCode()
			if statusCode >= 500 {
				return nil, fiber.NewError(statusCode, "upstream error")
			}

			return nil, err
		})

		if err != nil {
			// Circuit is open
			if err == gobreaker.ErrOpenState {
				return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
					"error":   "service_unavailable",
					"message": "Service temporarily unavailable, please try again later",
					"service": serviceName,
				})
			}

			// Circuit is half-open but request failed
			if err == gobreaker.ErrTooManyRequests {
				return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
					"error":   "too_many_requests",
					"message": "Service is recovering, please try again",
					"service": serviceName,
				})
			}

			// Other errors - response may already be set by c.Next()
			return nil
		}

		_ = result
		return nil
	}
}

// CircuitBreakerStats holds statistics for a circuit breaker
type CircuitBreakerStats struct {
	Name             string `json:"name"`
	State            string `json:"state"`
	Requests         uint32 `json:"requests"`
	Successes        uint32 `json:"successes"`
	Failures         uint32 `json:"failures"`
	ConsecutiveFails uint32 `json:"consecutive_failures"`
}

// RetryMiddleware provides retry logic for failed requests
type RetryMiddleware struct {
	MaxRetries  int
	WaitTime    time.Duration
	MaxWaitTime time.Duration
}

// NewRetryMiddleware creates a new retry middleware
func NewRetryMiddleware(maxRetries int, waitTime time.Duration) *RetryMiddleware {
	return &RetryMiddleware{
		MaxRetries:  maxRetries,
		WaitTime:    waitTime,
		MaxWaitTime: 30 * time.Second,
	}
}

// Middleware returns the retry middleware handler
func (rm *RetryMiddleware) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Check if route has retry config
		var maxAttempts int
		var waitTime time.Duration

		if route, ok := c.Locals("route").(config.Route); ok && route.Retry != nil {
			maxAttempts = route.Retry.MaxAttempts
			if d, err := time.ParseDuration(route.Retry.WaitTime); err == nil {
				waitTime = d
			}
		}

		if maxAttempts == 0 {
			maxAttempts = rm.MaxRetries
		}
		if waitTime == 0 {
			waitTime = rm.WaitTime
		}

		var lastErr error
		for attempt := 0; attempt <= maxAttempts; attempt++ {
			err := c.Next()

			// Success or client error - don't retry
			statusCode := c.Response().StatusCode()
			if statusCode < 500 {
				return err
			}

			lastErr = err

			// Don't wait after last attempt
			if attempt < maxAttempts {
				// Exponential backoff
				sleepTime := waitTime * time.Duration(1<<attempt)
				if sleepTime > rm.MaxWaitTime {
					sleepTime = rm.MaxWaitTime
				}
				time.Sleep(sleepTime)
			}
		}

		return lastErr
	}
}
