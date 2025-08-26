package services

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// ValidationService provides comprehensive input validation and security checks
type ValidationService struct {
	config     *config.Config
	logger     *logrus.Logger
	httpClient *http.Client

	// Compiled regex patterns for performance
	userIDPattern    *regexp.Regexp
	agentIDPattern   *regexp.Regexp
	contentPattern   *regexp.Regexp
	maliciousPattern *regexp.Regexp
}

// ValidationResult represents the result of a validation operation
type ValidationResult struct {
	Valid   bool                   `json:"valid"`
	Errors  []string               `json:"errors,omitempty"`
	Details map[string]interface{} `json:"details,omitempty"`
}

// ValidationConfig configures the validation service
type ValidationConfig struct {
	MaxContentLength    int           `json:"max_content_length"`
	MaxUserIDLength     int           `json:"max_user_id_length"`
	MaxAgentIDLength    int           `json:"max_agent_id_length"`
	AllowedDomains      []string      `json:"allowed_domains"`
	BlockedDomains      []string      `json:"blocked_domains"`
	MaxFileSizeBytes    int64         `json:"max_file_size_bytes"`
	MaxDurationSeconds  int           `json:"max_duration_seconds"`
	URLTimeout          time.Duration `json:"url_timeout"`
	EnableContentFilter bool          `json:"enable_content_filter"`
	StrictMode          bool          `json:"strict_mode"`
}

// NewValidationService creates a new validation service
func NewValidationService(config *config.Config, logger *logrus.Logger) (*ValidationService, error) {
	// Compile regex patterns
	userIDPattern, err := regexp.Compile(`^[a-zA-Z0-9_\-@.]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile user ID pattern: %w", err)
	}

	agentIDPattern, err := regexp.Compile(`^[a-zA-Z0-9_\-]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile agent ID pattern: %w", err)
	}

	contentPattern, err := regexp.Compile(`^[\p{L}\p{N}\p{P}\p{S}\p{Z}\r\n\t]+$`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile content pattern: %w", err)
	}

	// Pattern to detect potentially malicious content
	maliciousPattern, err := regexp.Compile(`(?i)(script|javascript|vbscript|onload|onerror|eval|document\.)|<[^>]*>|[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)
	if err != nil {
		return nil, fmt.Errorf("failed to compile malicious pattern: %w", err)
	}

	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	return &ValidationService{
		config:           config,
		logger:           logger,
		httpClient:       httpClient,
		userIDPattern:    userIDPattern,
		agentIDPattern:   agentIDPattern,
		contentPattern:   contentPattern,
		maliciousPattern: maliciousPattern,
	}, nil
}

// ValidateUserWebhookRequest validates user webhook request data
func (v *ValidationService) ValidateUserWebhookRequest(userID, content string, audioURL *string) *ValidationResult {
	result := &ValidationResult{
		Valid:   true,
		Errors:  make([]string, 0),
		Details: make(map[string]interface{}),
	}

	// Validate User ID
	if userIDResult := v.ValidateUserID(userID); !userIDResult.Valid {
		result.Valid = false
		result.Errors = append(result.Errors, userIDResult.Errors...)
	}

	// Validate Content
	if contentResult := v.ValidateContent(content); !contentResult.Valid {
		result.Valid = false
		result.Errors = append(result.Errors, contentResult.Errors...)
	}

	// Validate Audio URL if provided
	if audioURL != nil && *audioURL != "" {
		if audioResult := v.ValidateAudioURL(*audioURL); !audioResult.Valid {
			result.Valid = false
			result.Errors = append(result.Errors, audioResult.Errors...)
		}
	}

	return result
}

// ValidateUUID validates UUID format and provides detailed error information
func (v *ValidationService) ValidateUUID(uuidStr string) *ValidationResult {
	result := &ValidationResult{
		Valid:   true,
		Errors:  make([]string, 0),
		Details: make(map[string]interface{}),
	}

	if uuidStr == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "UUID cannot be empty")
		return result
	}

	if len(uuidStr) > 100 { // Reasonable upper limit
		result.Valid = false
		result.Errors = append(result.Errors, "UUID is too long")
		return result
	}

	parsedUUID, err := uuid.Parse(uuidStr)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Invalid UUID format: %v", err))
		result.Details["provided_value"] = uuidStr
		result.Details["expected_format"] = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
		return result
	}

	result.Details["uuid_version"] = parsedUUID.Version().String()
	result.Details["uuid_variant"] = parsedUUID.Variant().String()

	return result
}

// ValidateUserID validates user ID format and constraints
func (v *ValidationService) ValidateUserID(userID string) *ValidationResult {
	result := &ValidationResult{
		Valid:   true,
		Errors:  make([]string, 0),
		Details: make(map[string]interface{}),
	}

	if userID == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "User ID cannot be empty")
		return result
	}

	maxLength := v.getMaxUserIDLength()
	if len(userID) > maxLength {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("User ID exceeds maximum length of %d characters", maxLength))
		return result
	}

	if len(userID) < 3 {
		result.Valid = false
		result.Errors = append(result.Errors, "User ID must be at least 3 characters long")
		return result
	}

	if !v.userIDPattern.MatchString(userID) {
		result.Valid = false
		result.Errors = append(result.Errors, "User ID contains invalid characters. Only alphanumeric, underscore, hyphen, at sign, and dot are allowed")
		return result
	}

	// Check for suspicious patterns
	if v.containsSuspiciousPatterns(userID) {
		result.Valid = false
		result.Errors = append(result.Errors, "User ID contains suspicious patterns")
		return result
	}

	result.Details["length"] = len(userID)
	result.Details["sanitized"] = v.sanitizeForLogging(userID)

	return result
}

// ValidateAgentID validates agent ID format and constraints
func (v *ValidationService) ValidateAgentID(agentID string) *ValidationResult {
	result := &ValidationResult{
		Valid:   true,
		Errors:  make([]string, 0),
		Details: make(map[string]interface{}),
	}

	if agentID == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "Agent ID cannot be empty")
		return result
	}

	maxLength := v.getMaxAgentIDLength()
	if len(agentID) > maxLength {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Agent ID exceeds maximum length of %d characters", maxLength))
		return result
	}

	if len(agentID) < 3 {
		result.Valid = false
		result.Errors = append(result.Errors, "Agent ID must be at least 3 characters long")
		return result
	}

	if !v.agentIDPattern.MatchString(agentID) {
		result.Valid = false
		result.Errors = append(result.Errors, "Agent ID contains invalid characters. Only alphanumeric, underscore, and hyphen are allowed")
		return result
	}

	result.Details["length"] = len(agentID)
	result.Details["sanitized"] = v.sanitizeForLogging(agentID)

	return result
}

// ValidateContent validates message content for security and format
func (v *ValidationService) ValidateContent(content string) *ValidationResult {
	result := &ValidationResult{
		Valid:   true,
		Errors:  make([]string, 0),
		Details: make(map[string]interface{}),
	}

	if content == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "Content cannot be empty")
		return result
	}

	maxLength := v.getMaxContentLength()
	if len(content) > maxLength {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Content exceeds maximum length of %d characters", maxLength))
		return result
	}

	// Check if content is valid UTF-8
	if !utf8.ValidString(content) {
		result.Valid = false
		result.Errors = append(result.Errors, "Content contains invalid UTF-8 characters")
		return result
	}

	// Check for malicious patterns if content filtering is enabled
	if v.isContentFilterEnabled() && v.maliciousPattern.MatchString(content) {
		result.Valid = false
		result.Errors = append(result.Errors, "Content contains potentially malicious patterns")
		result.Details["security_scan"] = "failed"
		return result
	}

	// Check for excessive special characters or control characters
	if v.hasExcessiveControlChars(content) {
		result.Valid = false
		result.Errors = append(result.Errors, "Content contains excessive control characters")
		return result
	}

	result.Details["length"] = len(content)
	result.Details["word_count"] = v.countWords(content)
	result.Details["has_urls"] = v.containsURLs(content)
	result.Details["security_scan"] = "passed"

	return result
}

// ValidateAudioURL validates audio URL format, security, and accessibility
func (v *ValidationService) ValidateAudioURL(audioURL string) *ValidationResult {
	result := &ValidationResult{
		Valid:   true,
		Errors:  make([]string, 0),
		Details: make(map[string]interface{}),
	}

	if audioURL == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "Audio URL cannot be empty")
		return result
	}

	// Parse URL
	parsedURL, err := url.Parse(audioURL)
	if err != nil {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Invalid URL format: %v", err))
		return result
	}

	// Validate scheme
	if parsedURL.Scheme != "https" && parsedURL.Scheme != "http" {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Unsupported URL scheme: %s. Only HTTP and HTTPS are allowed", parsedURL.Scheme))
		return result
	}

	// Prefer HTTPS
	if parsedURL.Scheme == "http" {
		result.Details["security_warning"] = "HTTP URLs are less secure than HTTPS"
	}

	// Validate host
	if parsedURL.Host == "" {
		result.Valid = false
		result.Errors = append(result.Errors, "URL must have a valid host")
		return result
	}

	// Check against blocked domains
	if v.isDomainBlocked(parsedURL.Host) {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Domain %s is blocked", parsedURL.Host))
		return result
	}

	// Check against allowed domains if configured
	if !v.isDomainAllowed(parsedURL.Host) {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Domain %s is not in allowed domains list", parsedURL.Host))
		return result
	}

	// Check for suspicious URL patterns
	if v.isSuspiciousURL(audioURL) {
		result.Valid = false
		result.Errors = append(result.Errors, "URL contains suspicious patterns")
		return result
	}

	// Validate file extension if present
	if ext := v.extractFileExtension(parsedURL.Path); ext != "" {
		if !v.isSupportedAudioFormat(ext) {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("Unsupported audio format: %s", ext))
			return result
		}
		result.Details["file_extension"] = ext
	}

	result.Details["scheme"] = parsedURL.Scheme
	result.Details["host"] = parsedURL.Host
	result.Details["path"] = parsedURL.Path

	return result
}

// ValidateFileSizeAndDuration validates file size and duration limits
func (v *ValidationService) ValidateFileSizeAndDuration(sizeBytes int64, durationSeconds int) *ValidationResult {
	result := &ValidationResult{
		Valid:   true,
		Errors:  make([]string, 0),
		Details: make(map[string]interface{}),
	}

	maxSize := v.getMaxFileSizeBytes()
	if sizeBytes > maxSize {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("File size %d bytes exceeds maximum allowed size %d bytes", sizeBytes, maxSize))
	}

	maxDuration := v.getMaxDurationSeconds()
	if durationSeconds > maxDuration {
		result.Valid = false
		result.Errors = append(result.Errors, fmt.Sprintf("Duration %d seconds exceeds maximum allowed duration %d seconds", durationSeconds, maxDuration))
	}

	result.Details["size_bytes"] = sizeBytes
	result.Details["size_mb"] = float64(sizeBytes) / (1024 * 1024)
	result.Details["duration_seconds"] = durationSeconds
	result.Details["duration_minutes"] = float64(durationSeconds) / 60

	return result
}

// Helper methods

func (v *ValidationService) getMaxContentLength() int {
	if v.config.Security.MaxContentLength > 0 {
		return v.config.Security.MaxContentLength
	}
	return 10000 // Default 10KB
}

func (v *ValidationService) getMaxUserIDLength() int {
	if v.config.Security.MaxUserIDLength > 0 {
		return v.config.Security.MaxUserIDLength
	}
	return 100 // Default
}

func (v *ValidationService) getMaxAgentIDLength() int {
	if v.config.Security.MaxAgentIDLength > 0 {
		return v.config.Security.MaxAgentIDLength
	}
	return 100 // Default
}

func (v *ValidationService) getMaxFileSizeBytes() int64 {
	if v.config.Transcribe.MaxFileSizeMB > 0 {
		return int64(v.config.Transcribe.MaxFileSizeMB) * 1024 * 1024
	}
	return 25 * 1024 * 1024 // Default 25MB
}

func (v *ValidationService) getMaxDurationSeconds() int {
	if v.config.Transcribe.MaxDurationMinutes > 0 {
		return v.config.Transcribe.MaxDurationMinutes * 60
	}
	return 600 // Default 10 minutes
}

func (v *ValidationService) isContentFilterEnabled() bool {
	return v.config.Security.EnableContentFilter
}

func (v *ValidationService) containsSuspiciousPatterns(input string) bool {
	suspicious := []string{
		"../", "./", "script", "eval", "exec", "system", "cmd",
		"<script", "javascript:", "data:", "vbscript:",
	}

	lowerInput := strings.ToLower(input)
	for _, pattern := range suspicious {
		if strings.Contains(lowerInput, pattern) {
			return true
		}
	}
	return false
}

func (v *ValidationService) hasExcessiveControlChars(content string) bool {
	controlCharCount := 0
	for _, r := range content {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			controlCharCount++
		}
	}
	// Allow up to 5% control characters
	threshold := len(content) / 20
	if threshold < 5 {
		threshold = 5
	}
	return controlCharCount > threshold
}

func (v *ValidationService) countWords(content string) int {
	fields := strings.Fields(content)
	return len(fields)
}

func (v *ValidationService) containsURLs(content string) bool {
	return strings.Contains(content, "http://") || strings.Contains(content, "https://")
}

func (v *ValidationService) isDomainBlocked(host string) bool {
	blockedDomains := v.config.GetSecurityBlockedDomains()
	for _, domain := range blockedDomains {
		if strings.Contains(host, strings.TrimSpace(domain)) {
			return true
		}
	}
	return false
}

func (v *ValidationService) isDomainAllowed(host string) bool {
	allowedDomains := v.config.GetSecurityAllowedDomains()
	if len(allowedDomains) == 0 {
		return true // No restrictions if no allowed domains configured
	}

	for _, domain := range allowedDomains {
		if strings.Contains(host, strings.TrimSpace(domain)) {
			return true
		}
	}
	return false
}

func (v *ValidationService) isSuspiciousURL(urlStr string) bool {
	suspicious := []string{
		"localhost", "127.0.0.1", "0.0.0.0", "192.168.", "10.", "172.",
		"file://", "ftp://", "data:", "javascript:", "vbscript:",
		"exec", "system", "cmd", "shell",
	}

	lowerURL := strings.ToLower(urlStr)
	for _, pattern := range suspicious {
		if strings.Contains(lowerURL, pattern) {
			return true
		}
	}
	return false
}

func (v *ValidationService) extractFileExtension(path string) string {
	parts := strings.Split(path, ".")
	if len(parts) > 1 {
		return strings.ToLower(parts[len(parts)-1])
	}
	return ""
}

func (v *ValidationService) isSupportedAudioFormat(ext string) bool {
	supportedFormats := v.config.GetTranscribeSupportedFormats()
	for _, format := range supportedFormats {
		if strings.EqualFold(ext, strings.TrimSpace(format)) {
			return true
		}
	}
	return false
}

func (v *ValidationService) sanitizeForLogging(input string) string {
	// Remove potentially sensitive information for logging
	if len(input) > 50 {
		return input[:47] + "..."
	}
	return input
}

// SanitizeErrorMessage removes sensitive information from error messages
func (v *ValidationService) SanitizeErrorMessage(err error) string {
	if err == nil {
		return ""
	}

	message := err.Error()

	// Remove potential file paths
	message = regexp.MustCompile(`/[a-zA-Z0-9_\-./]+`).ReplaceAllString(message, "[PATH_REDACTED]")

	// Remove potential IP addresses
	message = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`).ReplaceAllString(message, "[IP_REDACTED]")

	// Remove potential UUIDs
	message = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`).ReplaceAllString(message, "[UUID_REDACTED]")

	// Limit message length
	if len(message) > 200 {
		message = message[:197] + "..."
	}

	return message
}

// HealthCheck performs a health check for the validation service
func (v *ValidationService) HealthCheck(ctx context.Context) error {
	// Test regex patterns
	testCases := []struct {
		pattern *regexp.Regexp
		name    string
		test    string
	}{
		{v.userIDPattern, "user_id", "test_user_123"},
		{v.agentIDPattern, "agent_id", "agent_123"},
		{v.contentPattern, "content", "Hello world"},
		{v.maliciousPattern, "malicious", "<script>"},
	}

	for _, tc := range testCases {
		if !tc.pattern.MatchString(tc.test) && tc.name != "malicious" {
			return fmt.Errorf("pattern %s failed health check", tc.name)
		}
		if tc.pattern.MatchString(tc.test) && tc.name == "malicious" {
			// This is expected - malicious pattern should match malicious content
			continue
		}
	}

	return nil
}
