package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/callbackguard"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// CallbackService handles callback URL execution with retry logic
type CallbackService struct {
	logger           *logrus.Logger
	config           *config.Config
	httpClient       *http.Client
	tracer           trace.Tracer
	dataRelayService *DataRelayService
}

// NewCallbackService creates a new callback service
func NewCallbackService(logger *logrus.Logger, cfg *config.Config, tracer trace.Tracer, dataRelayService *DataRelayService) *CallbackService {
	s := &CallbackService{
		logger:           logger,
		config:           cfg,
		tracer:           tracer,
		dataRelayService: dataRelayService,
	}
	s.httpClient = newCallbackHTTPClient(time.Duration(cfg.Callback.Timeout)*time.Second, s.checkRedirect)
	return s
}

// newCallbackHTTPClient monta o cliente HTTP dos callbacks com a proteção
// anti-SSRF no dial: cada conexão confere o IP já resolvido
// (callbackguard.DialControl) e recusa loopback, redes privadas, link-local
// (metadata server) e faixas reservadas, o que fecha o DNS rebinding.
//
// Transport.Proxy fica nil de propósito: com proxy, o dial iria ao proxy e o
// guarda conferiria o IP dele, não o do destino.
func newCallbackHTTPClient(timeout time.Duration, checkRedirect func(*http.Request, []*http.Request) error) *http.Client {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   callbackguard.DialControl,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
		CheckRedirect: checkRedirect,
	}
}

// maxCallbackRedirects é o mesmo limite padrão do net/http.
const maxCallbackRedirects = 10

// errRedirectRejected marca o redirect recusado: o destino não muda entre
// tentativas, então o envio não é retentado.
var errRedirectRejected = errors.New("callback redirect rejected")

// checkRedirect revalida o destino de cada redirect (esquema, localhost, IP
// literal e CALLBACK_ALLOWED_DOMAIN). O IP resolvido do novo destino é
// conferido de novo no dial.
func (s *CallbackService) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxCallbackRedirects {
		return fmt.Errorf("%w: stopped after %d redirects", errRedirectRejected, maxCallbackRedirects)
	}
	if _, err := callbackguard.ValidateURL(req.URL.String(), s.deliveryOptions()); err != nil {
		return fmt.Errorf("%w: %v", errRedirectRejected, err)
	}
	return nil
}

// deliveryOptions são as regras aplicadas no envio e nos redirects. Seguem as
// do webhook de entrada (handlers.validateCallbackURL), que não exige HTTPS:
// exigir aqui recusaria callbacks que o webhook já aceitou.
func (s *CallbackService) deliveryOptions() callbackguard.Options {
	return callbackguard.Options{
		AllowedDomains: callbackguard.ParseAllowedDomains(s.config.Callback.AllowedDomain),
	}
}

// ValidateCallbackURL validates that a callback URL meets security requirements:
// format, scheme (HTTPS when CALLBACK_REQUIRE_HTTPS), no localhost or
// non-public IP literal and CALLBACK_ALLOWED_DOMAIN. The resolved address is
// enforced when connecting (see newCallbackHTTPClient).
func (s *CallbackService) ValidateCallbackURL(callbackURL string) error {
	opts := s.deliveryOptions()
	opts.RequireHTTPS = s.config.Callback.RequireHTTPS
	_, err := callbackguard.ValidateURL(callbackURL, opts)
	return err
}

// ExecuteCallback sends a POST request to the callback URL with retry logic
func (s *CallbackService) ExecuteCallback(ctx context.Context, callbackURL string, payload models.CallbackPayload, userNumber string) error {
	// Não logar o callback_url (telefone do cidadão no path/query — LGPD);
	// message_id correlaciona out-of-band.
	logger := s.logger.WithFields(logrus.Fields{
		"message_id": payload.MessageID,
		"status":     payload.Status,
	})

	// Create span for callback execution
	var span trace.Span
	if s.tracer != nil {
		// callback.url omitido do span de propósito: ele vaza o telefone do
		// cidadão pro backend de tracing (LGPD). message.id basta pra correlação.
		ctx, span = s.tracer.Start(ctx, "execute_callback",
			trace.WithAttributes(
				attribute.String("message.id", payload.MessageID),
				attribute.String("message.status", payload.Status),
			),
		)
		defer span.End()
	}

	// Revalida o destino na entrega (allow-list e IP literal): a URL pode ter
	// vindo de uma mensagem enfileirada antes de CALLBACK_ALLOWED_DOMAIN mudar.
	if _, err := callbackguard.ValidateURL(callbackURL, s.deliveryOptions()); err != nil {
		logger.WithError(err).Warn("Callback URL rejected before delivery")
		if span != nil {
			span.SetAttributes(
				attribute.Bool("callback.success", false),
				attribute.String("callback.error", err.Error()),
			)
		}
		return fmt.Errorf("callback URL rejected: %w", err)
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

	// Send error to Data Relay error interceptor (async, non-blocking)
	if s.config.DataRelay.Enabled && s.dataRelayService != nil {
		go s.sendErrorInterceptor(ctx, callbackURL, payload, userNumber, err)
	}

	return fmt.Errorf("callback failed after %d attempts: %w", maxRetries+1, err)
}

// sanitizeURLError remove a URL crua de um *url.Error. O .Error() de um
// *url.Error (devolvido por http.NewRequestWithContext e httpClient.Do) ecoa a
// URL inteira — que carrega o telefone do cidadão no path/query (LGPD) — e esse
// erro flui pra logs, span OTel (callback.error) e corpo do Data Relay. Devolver
// a causa interna (.Err) preserva o motivo (timeout/DNS/conn recusada) sem a URL.
func sanitizeURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// sendCallbackRequest sends a single HTTP POST request to the callback URL
func (s *CallbackService) sendCallbackRequest(ctx context.Context, callbackURL string, payloadBytes []byte, logger *logrus.Entry) error {
	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", sanitizeURLError(err))
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
		return fmt.Errorf("request failed: %w", sanitizeURLError(err))
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
	// Destino não público ou redirect recusado não mudam entre tentativas.
	if errors.Is(err, callbackguard.ErrBlockedAddress) || errors.Is(err, errRedirectRejected) {
		return false
	}

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

// generateHMACSignature generates an HMAC-SHA256 signature for the payload
func generateHMACSignature(payload []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

// sendErrorInterceptor sends callback failure details to Data Relay
// This runs asynchronously and doesn't block the callback flow
func (s *CallbackService) sendErrorInterceptor(ctx context.Context, callbackURL string, payload models.CallbackPayload, userNumber string, callbackErr error) {
	// Sem callback_url (telefone do cidadão — LGPD); message_id correlaciona.
	logger := s.logger.WithFields(logrus.Fields{
		"message_id":     payload.MessageID,
		"user_number":    userNumber,
		"callback_error": callbackErr.Error(),
	})

	// Serialize the callback payload that failed
	inputBodyBytes, err := json.Marshal(payload)
	if err != nil {
		logger.WithError(err).Error("Failed to serialize callback payload for Data Relay")
		return
	}

	// Extract HTTP status code and error response from the callback error
	httpStatusCode := 0
	errorResponse := callbackErr.Error()

	if httpErr, ok := callbackErr.(*HTTPError); ok {
		httpStatusCode = httpErr.StatusCode
		errorResponse = httpErr.Body
	}

	// Build Data Relay error interceptor payload
	errorPayload := ErrorInterceptorPayload{
		CustomerWhatsappNumber: userNumber,
		Source:                 s.config.DataRelay.Source,
		FlowName:               s.config.DataRelay.FlowName,
		APIEndpoint:            callbackURL,
		InputBody:              string(inputBodyBytes),
		HTTPStatusCode:         httpStatusCode,
		ErrorResponse:          errorResponse,
	}

	// Send to Data Relay (with a separate timeout context)
	relayCtx, cancel := context.WithTimeout(context.Background(), time.Duration(s.config.DataRelay.Timeout)*time.Second)
	defer cancel()

	err = s.dataRelayService.SendErrorInterceptor(relayCtx, errorPayload)
	if err != nil {
		// Log but don't propagate - Data Relay is non-critical
		logger.WithError(err).Warn("Failed to send error interceptor to Data Relay")
		return
	}

	logger.Info("Successfully sent callback error to Data Relay error interceptor")
}
