package middleware

import (
	"fmt"
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/minisource/gateway/config"
)

// Logger interface for structured logging
type Logger interface {
	Debug(msg string, fields ...interface{})
	Info(msg string, fields ...interface{})
	Warn(msg string, fields ...interface{})
	Error(msg string, fields ...interface{})
}

// SimpleLogger is a basic logger implementation
type SimpleLogger struct {
	level  string
	format string
}

// NewLogger creates a new logger
func NewLogger(cfg config.LoggingConfig) *SimpleLogger {
	return &SimpleLogger{
		level:  cfg.Level,
		format: cfg.Format,
	}
}

func (l *SimpleLogger) Debug(msg string, fields ...interface{}) {
	if l.shouldLog("debug") {
		l.log("DEBUG", msg, fields...)
	}
}

func (l *SimpleLogger) Info(msg string, fields ...interface{}) {
	if l.shouldLog("info") {
		l.log("INFO", msg, fields...)
	}
}

func (l *SimpleLogger) Warn(msg string, fields ...interface{}) {
	if l.shouldLog("warn") {
		l.log("WARN", msg, fields...)
	}
}

func (l *SimpleLogger) Error(msg string, fields ...interface{}) {
	if l.shouldLog("error") {
		l.log("ERROR", msg, fields...)
	}
}

func (l *SimpleLogger) shouldLog(level string) bool {
	levels := map[string]int{
		"debug": 0,
		"info":  1,
		"warn":  2,
		"error": 3,
	}
	return levels[level] >= levels[l.level]
}

func (l *SimpleLogger) log(level, msg string, fields ...interface{}) {
	timestamp := time.Now().UTC().Format(time.RFC3339)

	if l.format == "json" {
		fmt.Fprintf(os.Stdout, `{"level":"%s","time":"%s","msg":"%s"`, level, timestamp, msg)
		for i := 0; i < len(fields)-1; i += 2 {
			fmt.Fprintf(os.Stdout, `,"%v":"%v"`, fields[i], fields[i+1])
		}
		fmt.Fprintln(os.Stdout, "}")
	} else {
		fmt.Fprintf(os.Stdout, "%s [%s] %s", timestamp, level, msg)
		for i := 0; i < len(fields)-1; i += 2 {
			fmt.Fprintf(os.Stdout, " %v=%v", fields[i], fields[i+1])
		}
		fmt.Fprintln(os.Stdout)
	}
}

// RequestLogger logs HTTP requests
func RequestLogger(logger Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		// Process request
		err := c.Next()

		// Calculate duration
		duration := time.Since(start)

		// Get request info
		requestID, _ := c.Locals("request_id").(string)
		userID, _ := c.Locals("user_id").(string)
		tenantID, _ := c.Locals("tenant_id").(string)
		service, _ := c.Locals("service").(string)

		status := c.Response().StatusCode()

		// Choose log level based on status
		logFn := logger.Info
		if status >= 500 {
			logFn = logger.Error
		} else if status >= 400 {
			logFn = logger.Warn
		}

		logFn("HTTP Request",
			"method", c.Method(),
			"path", c.Path(),
			"status", status,
			"duration_ms", duration.Milliseconds(),
			"ip", c.IP(),
			"request_id", requestID,
			"user_id", userID,
			"tenant_id", tenantID,
			"service", service,
			"user_agent", c.Get("User-Agent"),
		)

		return err
	}
}

// AccessLog creates an access log entry
type AccessLog struct {
	Timestamp    time.Time `json:"timestamp"`
	RequestID    string    `json:"request_id"`
	Method       string    `json:"method"`
	Path         string    `json:"path"`
	Status       int       `json:"status"`
	Duration     int64     `json:"duration_ms"`
	IP           string    `json:"ip"`
	UserAgent    string    `json:"user_agent"`
	UserID       string    `json:"user_id,omitempty"`
	TenantID     string    `json:"tenant_id,omitempty"`
	Service      string    `json:"service,omitempty"`
	RequestSize  int       `json:"request_size"`
	ResponseSize int       `json:"response_size"`
	Error        string    `json:"error,omitempty"`
}

// ErrorLogger logs errors with context
func ErrorLogger(logger Logger) fiber.ErrorHandler {
	return func(c *fiber.Ctx, err error) error {
		requestID, _ := c.Locals("request_id").(string)

		// Get status code from error
		code := fiber.StatusInternalServerError
		if e, ok := err.(*fiber.Error); ok {
			code = e.Code
		}

		logger.Error("Request error",
			"error", err.Error(),
			"status", code,
			"path", c.Path(),
			"method", c.Method(),
			"request_id", requestID,
			"ip", c.IP(),
		)

		// Return error response
		return c.Status(code).JSON(fiber.Map{
			"error":      "error",
			"message":    err.Error(),
			"request_id": requestID,
		})
	}
}
