package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// GovBrCallbackHandler handles Gov.br OAuth2/PKCE callback
type GovBrCallbackHandler struct {
	logger            *logrus.Logger
	config            *config.Config
	redisService      RedisServiceInterface // Main Redis (for auth state)
	govbrRedisService RedisServiceInterface // Gov.br specific Redis (for tokens, shared with MCP)
}

// GovBrTokenResponse represents the token response from Identidade Carioca
type GovBrTokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	IDToken          string `json:"id_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
}

// GovBrAuthState represents the auth state stored in Redis
type GovBrAuthState struct {
	UserNumber     string `json:"user_number"`
	CodeVerifier   string `json:"code_verifier"`
	ServiceContext string `json:"service_context"`
	CreatedAt      string `json:"created_at"`
	State          string `json:"state"`
}

// GovBrUserInfo represents user data from /userinfo endpoint
type GovBrUserInfo struct {
	Sub               string `json:"sub"`                // Subject identifier (usually CPF)
	Name              string `json:"name"`               // Full name
	Email             string `json:"email"`              // Email (optional)
	CPF               string `json:"cpf"`                // CPF (may be in sub or separate field)
	PreferredUsername string `json:"preferred_username"` // Common OIDC username claim
}

// NewGovBrCallbackHandler creates a new Gov.br callback handler
func NewGovBrCallbackHandler(
	logger *logrus.Logger,
	config *config.Config,
	redisService RedisServiceInterface,
	govbrRedisService RedisServiceInterface,
) *GovBrCallbackHandler {
	return &GovBrCallbackHandler{
		logger:            logger,
		config:            config,
		redisService:      redisService,
		govbrRedisService: govbrRedisService,
	}
}

// HandleCallback processes the OAuth2 callback from Identidade Carioca
//
// GET /auth/govbr/callback?code=ABC123&state=uuid
//
// Security:
//   - Validates state parameter against Redis
//   - Exchanges code for token using PKCE code_verifier
//   - Stores tokens with appropriate TTL
//   - Renders user-friendly success/error pages
//   - Audit logging
func (h *GovBrCallbackHandler) HandleCallback(c *gin.Context) {
	code := c.Query("code")
	authID := c.Query("state")
	errorParam := c.Query("error")
	errorDescription := c.Query("error_description")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	logger := h.logger.WithFields(logrus.Fields{
		"auth_id": authID,
		"handler": "govbr_callback",
	})

	// 1. Check for errors from provider
	if errorParam != "" {
		logger.WithFields(logrus.Fields{
			"error":             errorParam,
			"error_description": errorDescription,
		}).Warn("Error returned by OAuth provider")

		if errorDescription == "" {
			errorDescription = "Erro durante o processo de autenticação"
		}

		c.HTML(http.StatusOK, "govbr_auth_error.html", gin.H{
			"error":       errorParam,
			"description": errorDescription,
			"ttl_minutes": h.config.GovBr.AuthStateTTL / 60,
		})
		return
	}

	// 2. Validate parameters
	if code == "" || authID == "" {
		logger.Error("Invalid callback parameters: missing code or state")
		c.HTML(http.StatusBadRequest, "govbr_auth_error.html", gin.H{
			"error":       "invalid_request",
			"description": "Parâmetros de callback inválidos",
			"ttl_minutes": h.config.GovBr.AuthStateTTL / 60,
		})
		return
	}

	// 3. Retrieve auth state from Redis
	redisKey := fmt.Sprintf("govbr_auth:%s", authID)
	authStateJSON, err := h.redisService.Get(ctx, redisKey)
	if err != nil {
		logger.WithError(err).Error("Auth state not found or expired in Redis")
		c.HTML(http.StatusBadRequest, "govbr_auth_error.html", gin.H{
			"error": "expired_request",
			"description": "Sessão de autenticação expirada. " +
				"Por favor, retorne ao WhatsApp e inicie o processo novamente.",
			"ttl_minutes": h.config.GovBr.AuthStateTTL / 60,
		})
		return
	}

	var authState GovBrAuthState
	if err := json.Unmarshal([]byte(authStateJSON), &authState); err != nil {
		logger.WithError(err).Error("Failed to deserialize auth state")
		c.HTML(http.StatusInternalServerError, "govbr_auth_error.html", gin.H{
			"error":       "internal_error",
			"description": "Erro ao processar estado de autenticação",
			"ttl_minutes": h.config.GovBr.AuthStateTTL / 60,
		})
		return
	}

	if authState.State != "pending" {
		if authState.State == "completed" {
			logger.Debug("User clicked auth link again - checking if token still valid")

			tokenStillValid := h.isTokenValid(ctx, authState.UserNumber)

			if tokenStillValid {
				logger.Info("Token still valid - showing success page for repeated click")
				serviceName := formatServiceName(authState.ServiceContext)
				c.HTML(http.StatusOK, "govbr_auth_success.html", gin.H{
					"user_number": maskPhoneNumber(authState.UserNumber),
					"service":     serviceName,
				})
				return
			}

			logger.Info("Token expired - showing error page")
			c.HTML(http.StatusBadRequest, "govbr_auth_error.html", gin.H{
				"error":       "token_expired",
				"description": "Sua sessão expirou. Por favor, solicite um novo link de autenticação no WhatsApp.",
				"ttl_minutes": h.config.GovBr.AuthStateTTL / 60,
			})
			return
		}

		logger.WithField("state", authState.State).Warn("Auth state is not pending")
		c.HTML(http.StatusBadRequest, "govbr_auth_error.html", gin.H{
			"error":       "invalid_state",
			"description": "Estado de autenticação inválido",
			"ttl_minutes": h.config.GovBr.AuthStateTTL / 60,
		})
		return
	}

	logger = logger.WithFields(logrus.Fields{
		"user_number":     maskPhoneNumber(authState.UserNumber),
		"service_context": authState.ServiceContext,
	})

	// 4. Exchange authorization code for tokens (PKCE flow)
	tokenResp, err := h.exchangeCodeForToken(ctx, code, authState.CodeVerifier)
	if err != nil {
		logger.WithError(err).Error("Failed to exchange code for token")
		c.HTML(http.StatusInternalServerError, "govbr_auth_error.html", gin.H{
			"error":       "token_exchange_failed",
			"description": "Erro ao obter token de acesso. Tente novamente.",
			"ttl_minutes": h.config.GovBr.AuthStateTTL / 60,
		})
		return
	}

	// 5. Fetch user info from /userinfo endpoint (optional)
	userInfo, err := h.fetchUserInfo(ctx, tokenResp.AccessToken)
	if err != nil {
		logger.WithError(err).Warn("Failed to fetch user info (non-critical)")
		userInfo = nil
	}
	if idTokenUserInfo, err := parseUserInfoFromIDToken(tokenResp.IDToken); err == nil {
		userInfo = mergeUserInfo(userInfo, idTokenUserInfo)
	} else if tokenResp.IDToken != "" {
		logger.WithError(err).Warn("Failed to parse user info from id_token")
	}

	// 6. Store tokens and user info in Redis with appropriate TTL
	if err := h.storeTokens(ctx, authState.UserNumber, tokenResp, authState.ServiceContext, userInfo); err != nil {
		logger.WithError(err).Error("Failed to store tokens in Redis")
		c.HTML(http.StatusInternalServerError, "govbr_auth_error.html", gin.H{
			"error":       "storage_error",
			"description": "Erro ao armazenar credenciais",
			"ttl_minutes": h.config.GovBr.AuthStateTTL / 60,
		})
		return
	}

	// 7. Update auth state to "completed"
	authState.State = "completed"
	updatedStateJSON, _ := json.Marshal(authState)
	// Keep state for a bit longer for audit/debugging
	h.redisService.Set(ctx, redisKey, string(updatedStateJSON), 5*time.Minute)

	logger.Info("Gov.br authentication completed successfully")

	// 8. Render success page
	serviceContext := authState.ServiceContext
	serviceName := formatServiceName(serviceContext)

	c.HTML(http.StatusOK, "govbr_auth_success.html", gin.H{
		"user_number": maskPhoneNumber(authState.UserNumber),
		"service":     serviceName,
	})
}

// exchangeCodeForToken exchanges authorization code for access/refresh tokens
//
// Implements OAuth2 PKCE token exchange flow.
//
// Security:
//   - Uses code_verifier for PKCE validation
//   - Client credentials sent in POST body (not URL)
//   - Timeout to prevent hanging
//   - Validates response structure
func (h *GovBrCallbackHandler) exchangeCodeForToken(
	ctx context.Context,
	code string,
	codeVerifier string,
) (*GovBrTokenResponse, error) {
	tokenURL := h.config.GovBr.TokenEndpoint()

	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", h.config.GovBr.RedirectURI)
	data.Set("client_id", h.config.GovBr.ClientID)
	data.Set("client_secret", h.config.GovBr.ClientSecret)
	data.Set("code_verifier", codeVerifier) // PKCE proof

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		tokenURL,
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		h.logger.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
			"response":    string(body),
		}).Error("Token exchange returned non-200 status")
		return nil, fmt.Errorf("token exchange failed: %d - %s", resp.StatusCode, string(body))
	}

	var tokenResp GovBrTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}

	return &tokenResp, nil
}

// fetchUserInfo fetches user data from Gov.br /userinfo endpoint
//
// Uses the access_token to retrieve authenticated user information
// such as name, CPF, and email.
//
// Security:
//   - Access token sent as Bearer token in Authorization header
//   - Timeout to prevent hanging requests
//   - Validates response structure
func (h *GovBrCallbackHandler) fetchUserInfo(
	ctx context.Context,
	accessToken string,
) (*GovBrUserInfo, error) {
	userInfoURL := h.config.GovBr.UserInfoEndpoint()
	if userInfoURL == "" {
		h.logger.Debug("UserInfo URL not configured, skipping user data fetch")
		return nil, nil
	}

	// Create request with Bearer token
	req, err := http.NewRequestWithContext(
		ctx,
		"GET",
		userInfoURL,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create userinfo request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read userinfo response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		h.logger.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
		}).Warn("UserInfo request returned non-200 status")
		return nil, fmt.Errorf("userinfo request failed: %d", resp.StatusCode)
	}

	var userInfo GovBrUserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, fmt.Errorf("failed to parse userinfo response: %w", err)
	}

	// Normalize CPF (may be in sub or cpf field)
	if userInfo.CPF == "" && userInfo.Sub != "" {
		userInfo.CPF = userInfo.Sub
	}
	if userInfo.CPF == "" && userInfo.PreferredUsername != "" {
		userInfo.CPF = userInfo.PreferredUsername
	}

	h.logger.WithFields(logrus.Fields{
		"has_name":  userInfo.Name != "",
		"has_cpf":   userInfo.CPF != "",
		"has_email": userInfo.Email != "",
	}).Debug("User info fetched successfully")

	return &userInfo, nil
}

func parseUserInfoFromIDToken(idToken string) (*GovBrUserInfo, error) {
	if idToken == "" {
		return nil, nil
	}

	// The id_token has already been obtained through a successful token exchange.
	// We only use its claims as profile enrichment, not as an authorization proof.
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid id_token format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode id_token payload: %w", err)
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse id_token claims: %w", err)
	}

	userInfo := &GovBrUserInfo{
		Sub:               firstStringClaim(claims, "sub"),
		Name:              firstStringClaim(claims, "name", "nome"),
		Email:             firstStringClaim(claims, "email"),
		CPF:               firstStringClaim(claims, "cpf", "govbr_cpf", "preferred_username"),
		PreferredUsername: firstStringClaim(claims, "preferred_username"),
	}
	if userInfo.CPF == "" && userInfo.Sub != "" {
		userInfo.CPF = userInfo.Sub
	}

	return userInfo, nil
}

func mergeUserInfo(primary *GovBrUserInfo, fallback *GovBrUserInfo) *GovBrUserInfo {
	if primary == nil {
		return fallback
	}
	if fallback == nil {
		return primary
	}

	if primary.Sub == "" {
		primary.Sub = fallback.Sub
	}
	if primary.Name == "" {
		primary.Name = fallback.Name
	}
	if primary.Email == "" {
		primary.Email = fallback.Email
	}
	if primary.CPF == "" {
		primary.CPF = fallback.CPF
	}
	if primary.PreferredUsername == "" {
		primary.PreferredUsername = fallback.PreferredUsername
	}

	return primary
}

func firstStringClaim(claims map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := claims[key].(string)
		if ok && value != "" {
			return value
		}
	}

	return ""
}

// storeTokens stores OAuth tokens in Redis with appropriate TTL
//
// Security:
//   - Tokens stored with TTL based on expires_in
//   - Phone number sanitized before use as key
//   - Structured logging without exposing token values
func (h *GovBrCallbackHandler) storeTokens(
	ctx context.Context,
	userNumber string,
	tokenResp *GovBrTokenResponse,
	serviceContext string,
	userInfo *GovBrUserInfo,
) error {
	// Sanitize phone number for Redis key
	sanitizedPhone := sanitizePhoneNumber(userNumber)

	// Calculate expiration timestamps
	now := time.Now()
	expiresAt := now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Unix()
	refreshExpiresAt := now.Add(time.Duration(tokenResp.RefreshExpiresIn) * time.Second).Unix()

	// Prepare token data
	tokenData := map[string]interface{}{
		"access_token":       tokenResp.AccessToken,
		"refresh_token":      tokenResp.RefreshToken,
		"expires_at":         expiresAt,
		"refresh_expires_at": refreshExpiresAt,
		"token_type":         tokenResp.TokenType,
		"service_context":    serviceContext,
		"created_at":         now.UTC().Format(time.RFC3339),
	}

	// Add user info if available
	if userInfo != nil {
		safeUserInfo := map[string]interface{}{}
		if userInfo.Name != "" {
			safeUserInfo["nome"] = userInfo.Name
		}
		if userInfo.CPF != "" {
			safeUserInfo["cpf"] = userInfo.CPF
		}
		if userInfo.Email != "" {
			safeUserInfo["email"] = userInfo.Email
		}
		if len(safeUserInfo) > 0 {
			tokenData["user_info"] = safeUserInfo
		}
	}

	tokenJSON, err := json.Marshal(tokenData)
	if err != nil {
		return fmt.Errorf("failed to marshal token data: %w", err)
	}

	// Store in Gov.br Redis (shared with MCP). Keep the token record while the
	// refresh token can still recover the session.
	tokenKey := fmt.Sprintf("govbr_token:%s", sanitizedPhone)
	ttl := time.Duration(tokenResp.ExpiresIn) * time.Second
	if tokenResp.RefreshExpiresIn > tokenResp.ExpiresIn {
		ttl = time.Duration(tokenResp.RefreshExpiresIn) * time.Second
	}

	if err := h.govbrRedisService.Set(ctx, tokenKey, string(tokenJSON), ttl); err != nil {
		return fmt.Errorf("failed to store token in Gov.br Redis: %w", err)
	}

	h.logger.WithFields(logrus.Fields{
		"user":            maskPhoneNumber(userNumber),
		"service_context": serviceContext,
		"expires_in":      tokenResp.ExpiresIn,
	}).Info("Gov.br tokens stored successfully")

	return nil
}

// sanitizePhoneNumber removes non-numeric characters from phone number
//
// Security: Prevents Redis key injection by ensuring only digits
func sanitizePhoneNumber(phone string) string {
	var result strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// maskPhoneNumber masks part of phone number for logging
//
// Example: +5521999999999 -> +5521999***
func maskPhoneNumber(phone string) string {
	if len(phone) < 8 {
		return "****"
	}
	return phone[:len(phone)-6] + "***"
}

func (h *GovBrCallbackHandler) isTokenValid(ctx context.Context, userNumber string) bool {
	sanitizedPhone := sanitizePhoneNumber(userNumber)
	tokenKey := fmt.Sprintf("govbr_token:%s", sanitizedPhone)

	tokenJSON, err := h.govbrRedisService.Get(ctx, tokenKey)
	if err != nil {
		h.logger.WithError(err).Debug("Token not found in Redis")
		return false
	}

	var tokenData map[string]interface{}
	if err := json.Unmarshal([]byte(tokenJSON), &tokenData); err != nil {
		h.logger.WithError(err).Warn("Failed to parse token data")
		return false
	}

	expiresAt, ok := tokenData["expires_at"].(float64)
	if !ok {
		h.logger.Warn("Token missing expires_at field")
		return false
	}

	now := time.Now().Unix()
	isValid := now < int64(expiresAt)

	h.logger.WithFields(logrus.Fields{
		"expires_at": int64(expiresAt),
		"now":        now,
		"is_valid":   isValid,
	}).Debug("Token validation result")

	return isValid
}

// formatServiceName converts technical service_context to user-friendly name
func formatServiceName(serviceContext string) string {
	serviceNames := map[string]string{
		"consulta_dados":           "Consulta de Dados",
		"iptu":                     "IPTU",
		"multas":                   "Consulta de Multas",
		"processos":                "Processos",
		"consultas_gerais":         "Consultas Gerais",
		"chatbot-whatsapp":         "Chatbot WhatsApp",
		"chatbot-whatsapp-staging": "Chatbot WhatsApp (Staging)",
	}

	if name, ok := serviceNames[serviceContext]; ok {
		return name
	}

	// Fallback: se não encontrou, retorna nome genérico
	if serviceContext == "" {
		return "Serviço da Prefeitura"
	}

	// Se tem underscore, converte para espaço e Title Case
	return serviceContext
}
