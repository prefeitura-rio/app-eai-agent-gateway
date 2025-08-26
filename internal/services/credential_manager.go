package services

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// CredentialManager provides secure credential management and sanitization
type CredentialManager struct {
	config          *config.Config
	logger          *logrus.Logger
	sensitiveFields map[string]bool
	mu              sync.RWMutex

	// Compiled regex patterns for performance
	tokenPatterns    []*regexp.Regexp
	keyPatterns      []*regexp.Regexp
	passwordPatterns []*regexp.Regexp
	genericPatterns  []*regexp.Regexp
}

// SensitiveData represents potentially sensitive information
type SensitiveData struct {
	Found      bool     `json:"found"`
	FieldCount int      `json:"field_count"`
	Types      []string `json:"types,omitempty"`
}

// NewCredentialManager creates a new credential manager
func NewCredentialManager(config *config.Config, logger *logrus.Logger) (*CredentialManager, error) {
	// Define sensitive field names (case-insensitive)
	sensitiveFields := map[string]bool{
		"password":        true,
		"token":           true,
		"key":             true,
		"secret":          true,
		"auth":            true,
		"credential":      true,
		"authorization":   true,
		"bearer":          true,
		"api_key":         true,
		"apikey":          true,
		"private_key":     true,
		"service_account": true,
		"jwt":             true,
		"session":         true,
		"cookie":          true,
		"csrf":            true,
	}

	// Compile token patterns
	tokenPatterns := []*regexp.Regexp{
		// JWT tokens
		regexp.MustCompile(`\b[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
		// API keys (various formats)
		regexp.MustCompile(`\b[A-Za-z0-9]{20,}\b`),
		// Bearer tokens
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9_.-]+`),
		// Basic auth
		regexp.MustCompile(`(?i)basic\s+[A-Za-z0-9+/=]+`),
	}

	// Key patterns
	keyPatterns := []*regexp.Regexp{
		// Google service account keys
		regexp.MustCompile(`"private_key":\s*"[^"]+"`),
		regexp.MustCompile(`"private_key_id":\s*"[^"]+"`),
		// AWS keys
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`[A-Za-z0-9/+=]{40}`),
		// Generic hex keys
		regexp.MustCompile(`\b[0-9a-fA-F]{32,128}\b`),
	}

	// Password patterns
	passwordPatterns := []*regexp.Regexp{
		// Common password patterns
		regexp.MustCompile(`(?i)password["\s]*[:=]["\s]*[^\s"]+`),
		regexp.MustCompile(`(?i)pwd["\s]*[:=]["\s]*[^\s"]+`),
		regexp.MustCompile(`(?i)pass["\s]*[:=]["\s]*[^\s"]+`),
		// Generic token and key patterns
		regexp.MustCompile(`(?i)token["\s]*[:=]["\s]*[^\s"]+`),
		regexp.MustCompile(`(?i)api_key["\s]*[:=]["\s]*[^\s"]+`),
		regexp.MustCompile(`(?i)apikey["\s]*[:=]["\s]*[^\s"]+`),
		regexp.MustCompile(`(?i)secret["\s]*[:=]["\s]*[^\s"]+`),
		regexp.MustCompile(`(?i)key["\s]*[:=]["\s]*[^\s"]+`),
	}

	// Generic sensitive patterns
	genericPatterns := []*regexp.Regexp{
		// Credit card numbers
		regexp.MustCompile(`\b\d{4}[-\s]?\d{4}[-\s]?\d{4}[-\s]?\d{4}\b`),
		// Social security numbers
		regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
		// Email addresses (potential PII)
		regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Z|a-z]{2,}\b`),
	}

	return &CredentialManager{
		config:           config,
		logger:           logger,
		sensitiveFields:  sensitiveFields,
		tokenPatterns:    tokenPatterns,
		keyPatterns:      keyPatterns,
		passwordPatterns: passwordPatterns,
		genericPatterns:  genericPatterns,
	}, nil
}

// SanitizeString removes or redacts sensitive information from a string
func (cm *CredentialManager) SanitizeString(input string) string {
	if input == "" {
		return input
	}

	result := input

	// Sanitize tokens
	for _, pattern := range cm.tokenPatterns {
		result = pattern.ReplaceAllStringFunc(result, cm.redactMatch)
	}

	// Sanitize keys
	for _, pattern := range cm.keyPatterns {
		result = pattern.ReplaceAllStringFunc(result, cm.redactMatch)
	}

	// Sanitize passwords
	for _, pattern := range cm.passwordPatterns {
		result = pattern.ReplaceAllStringFunc(result, cm.redactPasswordMatch)
	}

	// Sanitize generic sensitive data if strict mode is enabled
	if cm.config.Security.StrictMode {
		for _, pattern := range cm.genericPatterns {
			result = pattern.ReplaceAllStringFunc(result, cm.redactMatch)
		}
	}

	return result
}

// SanitizeMap removes or redacts sensitive information from a map
func (cm *CredentialManager) SanitizeMap(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}

	result := make(map[string]interface{})

	for key, value := range input {
		sanitizedKey := strings.ToLower(key)

		// Check if field name indicates sensitive data
		if cm.isSensitiveFieldName(sanitizedKey) {
			result[key] = "[REDACTED]"
			continue
		}

		// Recursively sanitize based on value type
		switch v := value.(type) {
		case string:
			result[key] = cm.SanitizeString(v)
		case map[string]interface{}:
			result[key] = cm.SanitizeMap(v)
		case []interface{}:
			result[key] = cm.sanitizeSlice(v)
		default:
			result[key] = value
		}
	}

	return result
}

// SanitizeError removes sensitive information from error messages
func (cm *CredentialManager) SanitizeError(err error) string {
	if err == nil {
		return ""
	}

	message := err.Error()
	return cm.SanitizeString(message)
}

// SanitizeLogFields sanitizes log fields for safe logging
func (cm *CredentialManager) SanitizeLogFields(fields logrus.Fields) logrus.Fields {
	if fields == nil {
		return nil
	}

	sanitized := make(logrus.Fields)

	for key, value := range fields {
		sanitizedKey := strings.ToLower(key)

		// Check if field name indicates sensitive data
		if cm.isSensitiveFieldName(sanitizedKey) {
			sanitized[key] = "[REDACTED]"
			continue
		}

		// Sanitize string values
		if str, ok := value.(string); ok {
			sanitized[key] = cm.SanitizeString(str)
		} else {
			sanitized[key] = value
		}
	}

	return sanitized
}

// DetectSensitiveData analyzes text for potentially sensitive information
func (cm *CredentialManager) DetectSensitiveData(input string) *SensitiveData {
	result := &SensitiveData{
		Found: false,
		Types: make([]string, 0),
	}

	if input == "" {
		return result
	}

	// Check for tokens
	for _, pattern := range cm.tokenPatterns {
		if pattern.MatchString(input) {
			result.Found = true
			result.FieldCount++
			result.Types = append(result.Types, "token")
			break
		}
	}

	// Check for keys
	for _, pattern := range cm.keyPatterns {
		if pattern.MatchString(input) {
			result.Found = true
			result.FieldCount++
			result.Types = append(result.Types, "key")
			break
		}
	}

	// Check for passwords
	for _, pattern := range cm.passwordPatterns {
		if pattern.MatchString(input) {
			result.Found = true
			result.FieldCount++
			result.Types = append(result.Types, "password")
			break
		}
	}

	// Check for generic sensitive data if strict mode is enabled
	if cm.config.Security.StrictMode {
		for _, pattern := range cm.genericPatterns {
			if pattern.MatchString(input) {
				result.Found = true
				result.FieldCount++
				result.Types = append(result.Types, "pii")
				break
			}
		}
	}

	return result
}

// ValidateCredentialSecurity checks if credentials are being handled securely
func (cm *CredentialManager) ValidateCredentialSecurity(data map[string]interface{}) []string {
	var issues []string

	for key, value := range data {
		sanitizedKey := strings.ToLower(key)
		isSensitiveField := cm.isSensitiveFieldName(sanitizedKey)

		// Check for sensitive field names
		if isSensitiveField {
			// Check if value is properly redacted or empty
			if str, ok := value.(string); ok {
				if str != "" && str != "[REDACTED]" && !cm.isRedactedValue(str) {
					issues = append(issues, fmt.Sprintf("Sensitive field '%s' contains unredacted data", key))
				}
			}
		} else {
			// Only check for sensitive patterns in non-sensitive field names to avoid double-counting
			if str, ok := value.(string); ok {
				if sensitive := cm.DetectSensitiveData(str); sensitive.Found {
					issues = append(issues, fmt.Sprintf("Field '%s' contains potentially sensitive data: %v", key, sensitive.Types))
				}
			}
		}
	}

	return issues
}

// CreateSecureLogEntry creates a log entry with sanitized fields
func (cm *CredentialManager) CreateSecureLogEntry(logger *logrus.Entry, fields logrus.Fields) *logrus.Entry {
	sanitizedFields := cm.SanitizeLogFields(fields)
	return logger.WithFields(sanitizedFields)
}

// SafeString returns a safe version of a string for logging/display
func (cm *CredentialManager) SafeString(input string, maxLength int) string {
	if input == "" {
		return ""
	}

	// First sanitize sensitive content
	sanitized := cm.SanitizeString(input)

	// Then truncate if needed
	if maxLength > 0 && len(sanitized) > maxLength {
		if maxLength > 3 {
			return sanitized[:maxLength-3] + "..."
		}
		return sanitized[:maxLength]
	}

	return sanitized
}

// AddSensitiveField adds a new field name to be treated as sensitive
func (cm *CredentialManager) AddSensitiveField(fieldName string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	cm.sensitiveFields[strings.ToLower(fieldName)] = true
	cm.logger.WithField("field_name", fieldName).Debug("Added sensitive field")
}

// RemoveSensitiveField removes a field name from being treated as sensitive
func (cm *CredentialManager) RemoveSensitiveField(fieldName string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	delete(cm.sensitiveFields, strings.ToLower(fieldName))
	cm.logger.WithField("field_name", fieldName).Debug("Removed sensitive field")
}

// HealthCheck performs a health check for the credential manager
func (cm *CredentialManager) HealthCheck(ctx context.Context) error {
	// Test that patterns compile and work
	testString := "password=secret123 api_key=AKIA1234567890123456"
	sanitized := cm.SanitizeString(testString)

	if strings.Contains(sanitized, "secret123") || strings.Contains(sanitized, "AKIA1234567890123456") {
		return fmt.Errorf("credential sanitization is not working properly")
	}

	return nil
}

// Helper methods

func (cm *CredentialManager) isSensitiveFieldName(fieldName string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// Direct match
	if cm.sensitiveFields[fieldName] {
		return true
	}

	// Substring match for compound field names
	for sensitive := range cm.sensitiveFields {
		if strings.Contains(fieldName, sensitive) {
			return true
		}
	}

	return false
}

func (cm *CredentialManager) redactMatch(match string) string {
	if len(match) <= 8 {
		return "[REDACTED]"
	}

	// Show first 2 and last 2 characters with redaction in between
	return match[:2] + strings.Repeat("*", len(match)-4) + match[len(match)-2:]
}

func (cm *CredentialManager) redactPasswordMatch(match string) string {
	// For password fields, replace everything after the = or :
	parts := regexp.MustCompile(`[:=]`).Split(match, 2)
	if len(parts) == 2 {
		return parts[0] + ":[REDACTED]"
	}
	return "[REDACTED]"
}

func (cm *CredentialManager) sanitizeSlice(slice []interface{}) []interface{} {
	result := make([]interface{}, len(slice))

	for i, item := range slice {
		switch v := item.(type) {
		case string:
			result[i] = cm.SanitizeString(v)
		case map[string]interface{}:
			result[i] = cm.SanitizeMap(v)
		case []interface{}:
			result[i] = cm.sanitizeSlice(v)
		default:
			result[i] = item
		}
	}

	return result
}

func (cm *CredentialManager) isRedactedValue(value string) bool {
	redactedPatterns := []string{
		"[REDACTED]",
		"***",
		"****",
		"[HIDDEN]",
		"[PROTECTED]",
	}

	for _, pattern := range redactedPatterns {
		if strings.Contains(value, pattern) {
			return true
		}
	}

	return false
}
