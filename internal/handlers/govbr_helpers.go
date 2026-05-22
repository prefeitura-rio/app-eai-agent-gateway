package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// generatePKCEPair generates code_verifier and code_challenge for PKCE (RFC 7636)
//
// Returns:
//   - codeVerifier: 43-character URL-safe random string
//   - codeChallenge: SHA256 hash of verifier, base64url encoded
//   - error: if random generation fails
//
// Security:
//   - Uses crypto/rand for cryptographically secure random bytes
//   - code_verifier: 32 bytes = 43 chars base64url (no padding)
//   - code_challenge: SHA256(verifier), prevents code interception
func generatePKCEPair() (string, string, error) {
	// Generate 32 random bytes for code_verifier
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode as base64url without padding (43 characters)
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Generate code_challenge: SHA256(code_verifier)
	challengeBytes := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])

	return codeVerifier, codeChallenge, nil
}

// buildAuthURL constructs the full OAuth2 authorization URL with PKCE parameters
//
// Args:
//   - config: Gov.br OAuth2 configuration
//   - state: UUID for CSRF protection and session tracking
//   - codeChallenge: PKCE code challenge (SHA256 hash)
//
// Returns:
//
//	Full authorization URL with all required parameters
//
// URL Parameters:
//   - client_id: OAuth2 client identifier
//   - redirect_uri: Callback URL (must match registered URL)
//   - response_type: Always "code" for authorization code flow
//   - scope: Requested scopes (openid, profile, etc.)
//   - state: CSRF token / session ID
//   - code_challenge: PKCE challenge
//   - code_challenge_method: Always "S256" (SHA256)
//   - kc_idp_hint: Keycloak hint to use Gov.br IDP
func buildAuthURL(cfg *config.GovBrConfig, state, codeChallenge string) string {
	params := url.Values{}
	params.Set("client_id", cfg.ClientID)
	params.Set("redirect_uri", cfg.RedirectURI)
	params.Set("response_type", "code")
	params.Set("scope", cfg.Scope)
	params.Set("state", state)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	params.Set("kc_idp_hint", "govbr") // Force use of Gov.br identity provider

	return fmt.Sprintf("%s?%s", cfg.AuthURL, params.Encode())
}

// sanitizePhoneNumber removes all non-numeric characters from phone number
//
// Security: Prevents Redis key injection by ensuring only digits in the key
//
// Example:
//
//	"+5521999999999" -> "5521999999999"
//	"(21) 99999-9999" -> "2199999999"
func sanitizePhoneNumber(phone string) string {
	var result strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// maskPhoneNumber masks the last 6 digits of phone number for logging
//
// Security: Protects PII in logs while keeping enough digits for debugging
//
// Examples:
//
//	"+5521999999999" -> "+5521999***"
//	"2199999999" -> "2199***"
//	"123" -> "****" (too short)
func maskPhoneNumber(phone string) string {
	if len(phone) < 8 {
		return "****"
	}
	return phone[:len(phone)-6] + "***"
}
