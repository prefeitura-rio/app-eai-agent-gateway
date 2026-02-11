package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// DataRelayService handles communication with the Data Relay API
type DataRelayService struct {
	logger     *logrus.Logger
	config     *config.DataRelayConfig
	httpClient *http.Client
}

// ErrorInterceptorPayload represents the payload sent to Data Relay error interceptor
type ErrorInterceptorPayload struct {
	CustomerWhatsappNumber string `json:"customer_whatsapp_number"`
	Source                 string `json:"source"`
	FlowName               string `json:"flowname"`
	APIEndpoint            string `json:"api_endpoint"`
	InputBody              string `json:"input_body"`
	HTTPStatusCode         int    `json:"http_status_code"`
	ErrorResponse          string `json:"error_response"`
}

// NewDataRelayService creates a new Data Relay service
func NewDataRelayService(logger *logrus.Logger, cfg *config.DataRelayConfig) *DataRelayService {
	return &DataRelayService{
		logger: logger,
		config: cfg,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.Timeout) * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}
}

// SendErrorInterceptor sends callback error details to Data Relay
// This is a fire-and-forget operation - errors are logged but not propagated
func (s *DataRelayService) SendErrorInterceptor(ctx context.Context, payload ErrorInterceptorPayload) error {
	logger := s.logger.WithFields(logrus.Fields{
		"customer_number": payload.CustomerWhatsappNumber,
		"api_endpoint":    payload.APIEndpoint,
		"http_status":     payload.HTTPStatusCode,
		"source":          payload.Source,
		"flowname":        payload.FlowName,
	})

	// Validate configuration
	if s.config.BaseURL == "" {
		logger.Warn("Data Relay base URL not configured, skipping error interceptor")
		return fmt.Errorf("data relay base URL not configured")
	}

	if s.config.APIKey == "" {
		logger.Warn("Data Relay API key not configured, skipping error interceptor")
		return fmt.Errorf("data relay API key not configured")
	}

	// Build endpoint URL
	endpoint := fmt.Sprintf("%s/data/whatsapp-api-error-interceptor", s.config.BaseURL)

	// Serialize payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		logger.WithError(err).Error("Failed to serialize Data Relay error interceptor payload")
		return fmt.Errorf("failed to serialize payload: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		logger.WithError(err).Error("Failed to create Data Relay request")
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", s.config.APIKey)
	req.Header.Set("User-Agent", "EAI-Agent-Gateway/1.0")

	// Send request
	logger.Debug("Sending error interceptor to Data Relay")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		logger.WithError(err).Error("Failed to send error interceptor to Data Relay")
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.WithError(closeErr).Warn("Failed to close Data Relay response body")
		}
	}()

	// Read response body
	body, _ := io.ReadAll(resp.Body)

	// Check response status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logger.WithFields(logrus.Fields{
			"status_code":     resp.StatusCode,
			"response_body":   string(body),
			"response_length": len(body),
		}).Warn("Data Relay error interceptor returned non-2xx status")
		return fmt.Errorf("data relay returned status %d: %s", resp.StatusCode, string(body))
	}

	logger.WithFields(logrus.Fields{
		"status_code":     resp.StatusCode,
		"response_length": len(body),
	}).Info("Error interceptor sent to Data Relay successfully")

	return nil
}
