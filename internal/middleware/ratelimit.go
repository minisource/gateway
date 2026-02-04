package middleware

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/config"
	"github.com/redis/go-redis/v9"
)

// RateLimiter handles rate limiting
type RateLimiter struct {
	redis    *redis.Client
	cfg      config.RateLimitConfig
	local    *LocalLimiter
	useRedis bool
}

// LocalLimiter is an in-memory rate limiter fallback
type LocalLimiter struct {
	mu       sync.RWMutex
	requests map[string]*rateBucket
	cfg      config.RateLimitConfig
}

type rateBucket struct {
	tokens    float64
	lastCheck time.Time
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(cfg config.RateLimitConfig, redisClient *redis.Client) *RateLimiter {
	limiter := &RateLimiter{
		cfg:      cfg,
		redis:    redisClient,
		useRedis: redisClient != nil,
		local: &LocalLimiter{
			requests: make(map[string]*rateBucket),
			cfg:      cfg,
		},
	}

	// Start cleanup goroutine for local limiter
	if !limiter.useRedis {
		go limiter.local.cleanup(cfg.CleanupInterval)
	}

	return limiter
}

// Middleware returns the rate limiting middleware
func (rl *RateLimiter) Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !rl.cfg.Enabled {
			return c.Next()
		}

		// Get rate limit config (use route-specific if available)
		rps := rl.cfg.RequestsPerSec
		burst := rl.cfg.BurstSize

		if route, ok := c.Locals("route").(config.Route); ok {
			if route.RateLimit != nil {
				rps = route.RateLimit.RequestsPerSec
				burst = route.RateLimit.BurstSize
			}
		}

		// Create key (IP + optional user ID)
		key := rl.createKey(c)

		// Check rate limit
		allowed, remaining, resetTime := rl.allow(key, rps, burst)

		// Set rate limit headers
		c.Set("X-RateLimit-Limit", fmt.Sprintf("%d", rps))
		c.Set("X-RateLimit-Remaining", fmt.Sprintf("%d", remaining))
		c.Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetTime))

		if !allowed {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "rate_limit_exceeded",
				"message":     "Too many requests, please try again later",
				"retry_after": resetTime - time.Now().Unix(),
			})
		}

		return c.Next()
	}
}

// createKey creates a unique rate limit key
func (rl *RateLimiter) createKey(c *fiber.Ctx) string {
	// Use user ID if authenticated, otherwise IP
	if userID := c.Locals("user_id"); userID != nil {
		return fmt.Sprintf("ratelimit:%s:%s", userID, c.Path())
	}
	return fmt.Sprintf("ratelimit:ip:%s:%s", c.IP(), c.Path())
}

// allow checks if request is allowed (token bucket algorithm)
func (rl *RateLimiter) allow(key string, rps, burst int) (bool, int, int64) {
	if rl.useRedis {
		return rl.redisAllow(key, rps, burst)
	}
	return rl.local.allow(key, rps, burst)
}

// redisAllow implements rate limiting with Redis
func (rl *RateLimiter) redisAllow(key string, rps, burst int) (bool, int, int64) {
	ctx := context.Background()
	now := time.Now()

	// Token bucket with Redis
	script := redis.NewScript(`
		local key = KEYS[1]
		local rate = tonumber(ARGV[1])
		local burst = tonumber(ARGV[2])
		local now = tonumber(ARGV[3])
		local window = 1

		local data = redis.call('HMGET', key, 'tokens', 'last')
		local tokens = tonumber(data[1]) or burst
		local last = tonumber(data[2]) or now

		local elapsed = now - last
		tokens = math.min(burst, tokens + (elapsed * rate))

		local allowed = 0
		if tokens >= 1 then
			tokens = tokens - 1
			allowed = 1
		end

		redis.call('HMSET', key, 'tokens', tokens, 'last', now)
		redis.call('EXPIRE', key, window * 2)

		return {allowed, math.floor(tokens), now + (1 / rate)}
	`)

	result, err := script.Run(ctx, rl.redis, []string{key}, rps, burst, now.Unix()).Int64Slice()
	if err != nil {
		// Fallback to local limiter on Redis error
		return rl.local.allow(key, rps, burst)
	}

	return result[0] == 1, int(result[1]), result[2]
}

// allow implements local in-memory rate limiting
func (ll *LocalLimiter) allow(key string, rps, burst int) (bool, int, int64) {
	ll.mu.Lock()
	defer ll.mu.Unlock()

	now := time.Now()
	bucket, exists := ll.requests[key]

	if !exists {
		ll.requests[key] = &rateBucket{
			tokens:    float64(burst - 1),
			lastCheck: now,
		}
		return true, burst - 1, now.Add(time.Second).Unix()
	}

	// Add tokens based on elapsed time
	elapsed := now.Sub(bucket.lastCheck).Seconds()
	bucket.tokens = min(float64(burst), bucket.tokens+(elapsed*float64(rps)))
	bucket.lastCheck = now

	if bucket.tokens >= 1 {
		bucket.tokens--
		return true, int(bucket.tokens), now.Add(time.Second).Unix()
	}

	return false, 0, now.Add(time.Second / time.Duration(rps)).Unix()
}

// cleanup periodically removes old entries
func (ll *LocalLimiter) cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		ll.mu.Lock()
		threshold := time.Now().Add(-interval)
		for key, bucket := range ll.requests {
			if bucket.lastCheck.Before(threshold) {
				delete(ll.requests, key)
			}
		}
		ll.mu.Unlock()
	}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
