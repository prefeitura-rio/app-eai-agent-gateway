package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// CallbackService handles callback URL execution with retry logic
type CallbackService struct {
	logger     *logrus.Logger
	config     *config.Config
	httpClient *http.Client
	tracer     trace.Tracer
}

// NewCallbackService creates a new callback service
func NewCallbackService(logger *logrus.Logger, cfg *config.Config, tracer trace.Tracer) *CallbackService {
	return &CallbackService{
		logger: logger,
		config: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.Callback.Timeout) * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		tracer: tracer,
	}
}

// ValidateCallbackURL validates that a callback URL meets security requirements
func (s *CallbackService) ValidateCallbackURL(callbackURL string) error {
	// Check URL length
	if len(callbackURL) > 2048 {
		return fmt.Errorf("callback URL exceeds maximum length of 2048 characters")
	}

	// Parse URL
	parsedURL, err := url.Parse(callbackURL)
	if err != nil {
		return fmt.Errorf("invalid callback URL format: %w", err)
	}

	// Require HTTPS if configured
	if s.config.Callback.RequireHTTPS && parsedURL.Scheme != "https" {
		return fmt.Errorf("callback URL must use HTTPS protocol")
	}

	// Validate scheme
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("callback URL must use HTTP or HTTPS protocol")
	}

	// Check for localhost and private IP addresses
	host := parsedURL.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return fmt.Errorf("callback URL cannot use localhost")
	}

	// Check if host is an IP address and if it's private
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("callback URL cannot use private IP addresses")
		}
	}

	return nil
}

// ExecuteCallback sends a POST request to the callback URL with retry logic
func (s *CallbackService) ExecuteCallback(ctx context.Context, callbackURL string, payload models.CallbackPayload) error {
	logger := s.logger.WithFields(logrus.Fields{
		"callback_url": callbackURL,
		"message_id":   payload.MessageID,
		"status":       payload.Status,
	})

	// Create span for callback execution
	var span trace.Span
	if s.tracer != nil {
		ctx, span = s.tracer.Start(ctx, "execute_callback",
			trace.WithAttributes(
				attribute.String("callback.url", callbackURL),
				attribute.String("message.id", payload.MessageID),
				attribute.String("message.status", payload.Status),
			),
		)
		defer span.End()
	}

	// Serialize payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		logger.WithError(err).Error("Failed to serialize callback payload")
		if span != nil {
			span.SetAttributes(attribute.Bool("callback.success", false))
		}
		return fmt.Errorf("failed to serialize payload: %w", err)
	}

	// Retry logic with exponential backoff
	maxRetries := s.config.Callback.MaxRetries
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			logger.WithField("backoff_seconds", backoff.Seconds()).Info("Retrying callback after backoff")
			time.Sleep(backoff)
		}

		err = s.sendCallbackRequest(ctx, callbackURL, payloadBytes, logger)
		if err == nil {
			logger.Info("Callback executed successfully")
			if span != nil {
				span.SetAttributes(
					attribute.Bool("callback.success", true),
					attribute.Int("callback.attempts", attempt+1),
				)
			}
			return nil
		}

		// Check if error is retriable
		if !isRetriableError(err) {
			logger.WithError(err).Warn("Non-retriable error during callback execution")
			if span != nil {
				span.SetAttributes(
					attribute.Bool("callback.success", false),
					attribute.String("callback.error", err.Error()),
					attribute.Int("callback.attempts", attempt+1),
				)
			}
			return err
		}

		logger.WithError(err).WithField("attempt", attempt+1).Warn("Callback attempt failed, will retry")
	}

	logger.Error("Callback execution failed after all retry attempts")
	if span != nil {
		span.SetAttributes(
			attribute.Bool("callback.success", false),
			attribute.Int("callback.attempts", maxRetries+1),
		)
	}
	return fmt.Errorf("callback failed after %d attempts: %w", maxRetries+1, err)
}

// sendCallbackRequest sends a single HTTP POST request to the callback URL
func (s *CallbackService) sendCallbackRequest(ctx context.Context, callbackURL string, payloadBytes []byte, logger *logrus.Entry) error {
	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "EAI-Agent-Gateway/1.0")

	// Add Bearer Token authentication if configured
	if s.config.Callback.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.config.Callback.AuthToken)
	}

	// Add HMAC signature if enabled
	if s.config.Callback.EnableHMAC && s.config.Callback.HMACSecret != "" {
		signature := generateHMACSignature(payloadBytes, s.config.Callback.HMACSecret)
		req.Header.Set("X-Signature-SHA256", signature)
	}

	// Send request
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.WithError(closeErr).Warn("Failed to close response body")
		}
	}()

	// Read response body for logging
	body, _ := io.ReadAll(resp.Body)

	// Check response status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.WithFields(logrus.Fields{
			"status_code":     resp.StatusCode,
			"response_body":   string(body),
			"response_length": len(body),
		}).Warn("Callback returned non-2xx status code")

		return &HTTPError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	logger.WithFields(logrus.Fields{
		"status_code":     resp.StatusCode,
		"response_length": len(body),
	}).Debug("Callback request successful")

	return nil
}

// HTTPError represents an HTTP error response
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// isRetriableError determines if an error should trigger a retry
func isRetriableError(err error) bool {
	if httpErr, ok := err.(*HTTPError); ok {
		// Retry on 5xx errors and 429 (Too Many Requests)
		if httpErr.StatusCode >= 500 || httpErr.StatusCode == 429 {
			return true
		}
		// Don't retry on 4xx errors (except 429)
		return false
	}

	// Retry on network errors, timeouts, etc.
	return true
}

// isPrivateIP checks if an IP address is in a private range
func isPrivateIP(ip net.IP) bool {
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16", // Link-local
		"fc00::/7",       // IPv6 private
	}

	for _, cidr := range privateRanges {
		_, network, _ := net.ParseCIDR(cidr)
		if network.Contains(ip) {
			return true
		}
	}

	return false
}

// generateHMACSignature generates an HMAC-SHA256 signature for the payload
func generateHMACSignature(payload []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
