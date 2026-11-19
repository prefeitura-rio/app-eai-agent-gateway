package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Auth state lifecycle (govbr_auth:<state>):
// pending → awaiting_confirmation → completed | rejected.
const (
	govBrStatePending              = "pending"
	govBrStateAwaitingConfirmation = "awaiting_confirmation"
	govBrStateCompleted            = "completed"
	govBrStateRejected             = "rejected"
)

// Decisions accepted by POST /auth/govbr/confirm.
const (
	govBrDecisionConfirm = "confirm"
	govBrDecisionReject  = "reject"
)

const (
	// govBrConfirmTokenBytes is the entropy of the confirmation token. It is
	// encoded with base64.RawURLEncoding, so the token has 43 characters.
	govBrConfirmTokenBytes = 32
	govBrConfirmTokenLen   = 43

	govBrDefaultConfirmationTTL = 5 * time.Minute
	govBrMinConfirmationTTL     = time.Minute
	govBrFinalStateTTL          = 5 * time.Minute
)

// govBrPendingConfirmation is a finished gov.br login waiting for the citizen
// to confirm the WhatsApp number it will be bound to. Stored in the Gov.br
// Redis (where the tokens live) under govbr_confirm:<confirm token>.
type govBrPendingConfirmation struct {
	AuthID    string              `json:"auth_id"`
	AuthState GovBrAuthState      `json:"auth_state"`
	Token     *GovBrTokenResponse `json:"token"`
	UserInfo  *GovBrUserInfo      `json:"user_info,omitempty"`
}

// redisDeleter is implemented by services.RedisService. It is optional so the
// handler keeps depending only on RedisServiceInterface.
type redisDeleter interface {
	Delete(ctx context.Context, key string) error
}

// HandleConfirm receives the citizen's answer to the confirmation page shown
// by HandleCallback.
//
// POST /auth/govbr/confirm (form: confirm_token, decision=confirm|reject)
//
// Security (DEV-187):
//   - The confirmation token is generated in the callback, after the gov.br
//     login, and is only rendered in that page: whoever called /initiate never
//     sees it, so they cannot confirm a binding on the citizen's behalf.
//   - Single use: SETNX govbr_confirm_used:<token>; Redis errors fail closed.
//   - Only "confirm" writes govbr_token:<phone> and fires the auto-resume;
//     "reject" discards the tokens.
func (h *GovBrCallbackHandler) HandleConfirm(c *gin.Context) {
	confirmToken := c.PostForm("confirm_token")
	decision := c.PostForm("decision")

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	logger := h.logger.WithField("handler", "govbr_confirm")
	c.Header("Cache-Control", "no-store")

	if !isValidConfirmToken(confirmToken) ||
		(decision != govBrDecisionConfirm && decision != govBrDecisionReject) {
		logger.Warn("Invalid Gov.br confirmation request")
		h.renderAuthError(c, http.StatusBadRequest, "invalid_request", "Pedido de confirmação inválido")
		return
	}

	pendingKey := govBrConfirmKey(confirmToken)
	pendingJSON, err := h.govbrRedisService.Get(ctx, pendingKey)
	if err != nil {
		logger.Info("Gov.br confirmation not found or expired")
		h.renderAuthError(c, http.StatusBadRequest, "expired_request",
			"Confirmação expirada ou já utilizada. Por favor, retorne ao WhatsApp e inicie o processo novamente.")
		return
	}

	var pending govBrPendingConfirmation
	if err := json.Unmarshal([]byte(pendingJSON), &pending); err != nil || pending.Token == nil || pending.AuthID == "" {
		logger.Error("Failed to deserialize pending Gov.br confirmation")
		h.renderAuthError(c, http.StatusInternalServerError, "internal_error", "Erro ao processar a confirmação")
		return
	}

	logger = logger.WithFields(logrus.Fields{
		"auth_id":         pending.AuthID,
		"user_number":     maskPhoneNumber(pending.AuthState.UserNumber),
		"service_context": pending.AuthState.ServiceContext,
		"decision":        decision,
	})

	acquired, err := h.redisService.SetNX(ctx, govBrConfirmUsedKey(confirmToken), decision, h.confirmationTTL())
	if err != nil {
		logger.WithError(err).Error("Gov.br confirmation lock failed; refusing (fail closed)")
		h.renderAuthError(c, http.StatusInternalServerError, "internal_error", "Erro ao processar a confirmação. Tente novamente.")
		return
	}
	if !acquired {
		logger.Warn("Gov.br confirmation token reused")
		h.renderAuthError(c, http.StatusBadRequest, "already_used",
			"Esta confirmação já foi registrada. Retorne ao WhatsApp para continuar.")
		return
	}
	h.discardPendingConfirmation(ctx, pendingKey)

	if decision == govBrDecisionReject {
		pending.AuthState.State = govBrStateRejected
		h.saveAuthState(ctx, pending.AuthID, pending.AuthState, govBrFinalStateTTL)
		logger.Warn("Citizen rejected binding the gov.br identity to this phone number; tokens discarded")
		h.renderAuthError(c, http.StatusOK, "binding_rejected",
			"Vinculação cancelada: nenhum dado do seu gov.br foi associado a esse número. "+
				"Se você não pediu esta autenticação, ignore o link que recebeu.")
		return
	}

	if err := h.storeTokens(ctx, pending.AuthState.UserNumber, pending.Token, pending.AuthState.ServiceContext, pending.UserInfo); err != nil {
		logger.WithError(err).Error("Failed to store tokens in Redis")
		h.renderAuthError(c, http.StatusInternalServerError, "storage_error", "Erro ao armazenar credenciais")
		return
	}

	pending.AuthState.State = govBrStateCompleted
	// Keep state for a bit longer for audit/debugging
	h.saveAuthState(ctx, pending.AuthID, pending.AuthState, govBrFinalStateTTL)

	logger.Info("Gov.br authentication completed successfully")

	// Auto-resume: re-injeta a solicitação original no pipeline inbound para
	// o cidadão NÃO precisar mandar "ok". Best-effort — nunca falha a confirmação;
	// se falhar, o cidadão ainda pode mandar uma mensagem (fallback manual).
	// Em goroutine (contexto próprio dentro) pra não atrasar a página de sucesso
	// se o inbound/RabbitMQ estiver lento.
	go h.triggerAutoResume(pending.AuthID, pending.AuthState.UserNumber, pending.AuthState.ServiceContext)

	c.HTML(http.StatusOK, "govbr_auth_success.html", gin.H{
		"user_number": maskPhoneNumber(pending.AuthState.UserNumber),
		"service":     formatServiceName(pending.AuthState.ServiceContext),
	})
}

// createPendingConfirmation stores the exchanged tokens under a fresh
// single-use confirmation token and returns it.
func (h *GovBrCallbackHandler) createPendingConfirmation(
	ctx context.Context,
	authID string,
	authState GovBrAuthState,
	tokenResp *GovBrTokenResponse,
	userInfo *GovBrUserInfo,
) (string, error) {
	tokenBytes := make([]byte, govBrConfirmTokenBytes)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate confirmation token: %w", err)
	}
	confirmToken := base64.RawURLEncoding.EncodeToString(tokenBytes)

	pending := govBrPendingConfirmation{
		AuthID:    authID,
		AuthState: authState,
		Token:     tokenResp,
		UserInfo:  userInfo,
	}
	pendingJSON, err := json.Marshal(pending)
	if err != nil {
		return "", fmt.Errorf("failed to marshal pending confirmation: %w", err)
	}

	if err := h.govbrRedisService.Set(ctx, govBrConfirmKey(confirmToken), string(pendingJSON), h.confirmationTTL()); err != nil {
		return "", fmt.Errorf("failed to store pending confirmation: %w", err)
	}

	return confirmToken, nil
}

// discardPendingConfirmation removes the pending tokens once the confirmation
// was consumed. Best-effort: the record also expires with its TTL.
func (h *GovBrCallbackHandler) discardPendingConfirmation(ctx context.Context, pendingKey string) {
	deleter, ok := h.govbrRedisService.(redisDeleter)
	if !ok {
		return
	}
	if err := deleter.Delete(ctx, pendingKey); err != nil {
		h.logger.WithError(err).Warn("Failed to discard pending Gov.br confirmation; it expires with its TTL")
	}
}

// saveAuthState rewrites govbr_auth:<authID>. Best-effort: the state only
// drives the messages of a repeated click on the callback link.
func (h *GovBrCallbackHandler) saveAuthState(ctx context.Context, authID string, authState GovBrAuthState, ttl time.Duration) {
	stateJSON, err := json.Marshal(authState)
	if err != nil {
		h.logger.WithError(err).Warn("Failed to marshal Gov.br auth state")
		return
	}
	if err := h.redisService.Set(ctx, fmt.Sprintf("govbr_auth:%s", authID), string(stateJSON), ttl); err != nil {
		h.logger.WithError(err).Warn("Failed to update Gov.br auth state")
	}
}

// confirmationTTL is how long the citizen has to answer the confirmation page.
func (h *GovBrCallbackHandler) confirmationTTL() time.Duration {
	ttl := time.Duration(h.config.GovBr.AuthStateTTL) * time.Second
	if ttl <= 0 {
		return govBrDefaultConfirmationTTL
	}
	if ttl < govBrMinConfirmationTTL {
		return govBrMinConfirmationTTL
	}
	return ttl
}

func (h *GovBrCallbackHandler) renderAuthError(c *gin.Context, status int, code, description string) {
	c.HTML(status, "govbr_auth_error.html", gin.H{
		"error":       code,
		"description": description,
		"ttl_minutes": h.config.GovBr.AuthStateTTL / 60,
	})
}

func govBrConfirmKey(confirmToken string) string {
	return "govbr_confirm:" + confirmToken
}

func govBrConfirmUsedKey(confirmToken string) string {
	return "govbr_confirm_used:" + confirmToken
}

// isValidConfirmToken accepts only the shape createPendingConfirmation
// generates, so arbitrary input never reaches a Redis key.
func isValidConfirmToken(token string) bool {
	if len(token) != govBrConfirmTokenLen {
		return false
	}
	for _, r := range token {
		isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isAlnum && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// maskPhoneForConfirmation shows the country/area code and the last four
// digits, so the citizen can recognise their own number without the page
// exposing it whole. Example: +5521999991234 -> +5521*****1234.
func maskPhoneForConfirmation(phone string) string {
	digits := sanitizePhoneNumber(phone)
	if len(digits) < 10 {
		return "****"
	}
	const visiblePrefix, visibleSuffix = 4, 4
	hidden := len(digits) - visiblePrefix - visibleSuffix
	return "+" + digits[:visiblePrefix] + strings.Repeat("*", hidden) + digits[len(digits)-visibleSuffix:]
}
