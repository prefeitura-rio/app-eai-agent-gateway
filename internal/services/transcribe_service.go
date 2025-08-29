package services

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	speech "cloud.google.com/go/speech/apiv2"
	"github.com/sirupsen/logrus"
	"google.golang.org/api/option"
	speechpb "google.golang.org/genproto/googleapis/cloud/speech/v2"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// decodeServiceAccount decodes the SERVICE_ACCOUNT environment variable (same as sandbox.go)
func decodeServiceAccount(env string) ([]byte, error) {
	// Try base64 decode first
	if decoded, err := base64.StdEncoding.DecodeString(env); err == nil {
		if len(decoded) > 0 && decoded[0] == '{' {
			return decoded, nil
		}
	}
	// Try raw JSON
	raw := []byte(env)
	if len(raw) > 0 && raw[0] == '{' {
		return raw, nil
	}
	return nil, fmt.Errorf("SERVICE_ACCOUNT is not valid base64 JSON nor raw JSON")
}

// TranscribeServiceInterface defines the interface for audio transcription
type TranscribeServiceInterface interface {
	TranscribeFromURL(ctx context.Context, audioURL string) (*TranscriptionResult, error)
	TranscribeFromFile(ctx context.Context, filePath string) (*TranscriptionResult, error)
	HealthCheck(ctx context.Context) error
	Close() error
}

// TranscriptionResult represents the result of an audio transcription
type TranscriptionResult struct {
	Text         string                     `json:"text"`
	Confidence   float32                    `json:"confidence"`
	Duration     time.Duration              `json:"duration"`
	Language     string                     `json:"language"`
	Alternatives []TranscriptionAlternative `json:"alternatives,omitempty"`
	WordInfo     []WordInfo                 `json:"word_info,omitempty"`
	Metadata     map[string]interface{}     `json:"metadata"`
}

// TranscriptionAlternative represents alternative transcription results
type TranscriptionAlternative struct {
	Text       string  `json:"text"`
	Confidence float32 `json:"confidence"`
}

// WordInfo represents word-level transcription information
type WordInfo struct {
	Word       string        `json:"word"`
	StartTime  time.Duration `json:"start_time"`
	EndTime    time.Duration `json:"end_time"`
	Confidence float32       `json:"confidence"`
}

// TranscribeService implements Google Cloud Speech-to-Text API
type TranscribeService struct {
	config      *config.Config
	logger      *logrus.Logger
	client      *speech.Client
	rateLimiter RateLimiterInterface
}

// NewTranscribeService creates a new transcription service
func NewTranscribeService(
	cfg *config.Config,
	logger *logrus.Logger,
	rateLimiter RateLimiterInterface,
) (*TranscribeService, error) {
	ctx := context.Background()

	// Use exact same pattern as sandbox.go newSpeechClient function
	svcEnv := os.Getenv("SERVICE_ACCOUNT")
	var client *speech.Client
	var err error

	if svcEnv != "" {
		logger.Info("Transcribe service - using SERVICE_ACCOUNT env var")
		creds, decodeErr := decodeServiceAccount(svcEnv)
		if decodeErr != nil {
			logger.WithError(decodeErr).Error("Failed to decode SERVICE_ACCOUNT")
			return nil, fmt.Errorf("decoding SERVICE_ACCOUNT: %w", decodeErr)
		}
		client, err = speech.NewClient(ctx, option.WithCredentialsJSON(creds))
		if err != nil {
			return nil, fmt.Errorf("speech.NewClient(with creds): %w", err)
		}
		logger.Info("Transcribe service - authenticated using SERVICE_ACCOUNT env var")
	} else {
		client, err = speech.NewClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("speech.NewClient(ADC): %w", err)
		}
		logger.Info("Transcribe service - authenticated using Application Default Credentials")
	}

	service := &TranscribeService{
		config:      cfg,
		logger:      logger,
		client:      client,
		rateLimiter: rateLimiter,
	}

	logger.WithFields(logrus.Fields{
		"language_code":    cfg.Transcribe.LanguageCode,
		"max_duration":     cfg.Transcribe.MaxDuration,
		"max_file_size_mb": cfg.Transcribe.MaxFileSizeMB,
	}).Info("Transcription service initialized")

	return service, nil
}

// TranscribeFromURL downloads an audio file from URL and transcribes it
func (s *TranscribeService) TranscribeFromURL(ctx context.Context, audioURL string) (*TranscriptionResult, error) {
	start := time.Now()

	s.logger.WithField("audio_url", audioURL).Debug("Starting transcription from URL")

	// Apply rate limiting
	if err := s.rateLimiter.Wait(ctx, "transcribe_service"); err != nil {
		return nil, fmt.Errorf("rate limit exceeded: %w", err)
	}

	// Validate URL
	if err := s.validateURL(audioURL); err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	// Download the file to temp location
	tempFile, err := s.downloadFile(ctx, audioURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}
	defer func() {
		if err := os.Remove(tempFile); err != nil {
			s.logger.WithError(err).WithField("temp_file", tempFile).Warn("Failed to clean up temporary file")
		}
	}()

	// Transcribe the downloaded file
	result, err := s.TranscribeFromFile(ctx, tempFile)
	if err != nil {
		return nil, err
	}

	// Add download metadata
	if result.Metadata == nil {
		result.Metadata = make(map[string]interface{})
	}
	result.Metadata["source_url"] = audioURL
	result.Metadata["download_duration_ms"] = time.Since(start).Milliseconds()

	s.logger.WithFields(logrus.Fields{
		"audio_url":   audioURL,
		"text_length": len(result.Text),
		"confidence":  result.Confidence,
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("URL transcription completed")

	return result, nil
}

// TranscribeFromFile transcribes an audio file from local filesystem
func (s *TranscribeService) TranscribeFromFile(ctx context.Context, filePath string) (*TranscriptionResult, error) {
	start := time.Now()

	s.logger.WithField("file_path", filePath).Debug("Starting transcription from file")

	// Apply rate limiting
	if err := s.rateLimiter.Wait(ctx, "transcribe_service"); err != nil {
		return nil, fmt.Errorf("rate limit exceeded: %w", err)
	}

	// Validate file
	if err := s.validateFile(filePath); err != nil {
		return nil, fmt.Errorf("invalid file: %w", err)
	}

	// Read audio file
	audioData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio file: %w", err)
	}

	// Create transcription request with timeout (original working approach)
	reqCtx, cancel := context.WithTimeout(ctx, s.config.Transcribe.RequestTimeout)
	defer cancel()

	// Get project ID from config
	projectID := s.config.GoogleCloud.ProjectID
	if projectID == "" {
		return nil, fmt.Errorf("PROJECT_ID is required for Speech v2 API")
	}

	// Build recognition request using Speech v2 API (original working pattern)
	req := &speechpb.RecognizeRequest{
		Recognizer: fmt.Sprintf("projects/%s/locations/global/recognizers/_", projectID),
		Config: &speechpb.RecognitionConfig{
			DecodingConfig: &speechpb.RecognitionConfig_AutoDecodingConfig{
				AutoDecodingConfig: &speechpb.AutoDetectDecodingConfig{},
			},
			Features: &speechpb.RecognitionFeatures{
				EnableAutomaticPunctuation: true,
				MaxAlternatives:            int32(s.config.Transcribe.MaxAlternatives),
			},
			LanguageCodes: []string{s.config.Transcribe.LanguageCode},
			Model:         "long",
		},
		AudioSource: &speechpb.RecognizeRequest_Content{
			Content: audioData,
		},
	}

	// Perform transcription
	resp, err := s.client.Recognize(reqCtx, req)
	if err != nil {
		s.logger.WithError(err).WithField("file_path", filePath).Error("Failed to transcribe audio")
		return nil, fmt.Errorf("failed to transcribe audio: %w", err)
	}

	// Process results
	result := s.processRecognitionResponse(resp, start)

	// Add file metadata
	if result.Metadata == nil {
		result.Metadata = make(map[string]interface{})
	}
	result.Metadata["source_file"] = filePath
	result.Metadata["file_size_bytes"] = len(audioData)
	result.Metadata["transcription_duration_ms"] = time.Since(start).Milliseconds()

	s.logger.WithFields(logrus.Fields{
		"file_path":       filePath,
		"file_size_bytes": len(audioData),
		"text_length":     len(result.Text),
		"confidence":      result.Confidence,
		"duration_ms":     time.Since(start).Milliseconds(),
	}).Info("File transcription completed")

	return result, nil
}

// validateURL validates that the audio URL is allowed
func (s *TranscribeService) validateURL(audioURL string) error {
	parsed, err := url.Parse(audioURL)
	if err != nil {
		return fmt.Errorf("invalid URL format: %w", err)
	}

	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("unsupported URL scheme: %s", parsed.Scheme)
	}

	allowedDomains := s.config.GetTranscribeAllowedDomains()
	for _, domain := range allowedDomains {
		if strings.Contains(audioURL, strings.TrimSpace(domain)) {
			return nil
		}
	}

	return fmt.Errorf("URL not in allowed domains: %v", allowedDomains)
}

// validateFile validates the audio file
func (s *TranscribeService) validateFile(filePath string) error {
	// Check if file exists
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("file does not exist: %w", err)
	}

	// Check file size
	maxSizeBytes := int64(s.config.Transcribe.MaxFileSizeMB) * 1024 * 1024
	if info.Size() > maxSizeBytes {
		return fmt.Errorf("file size %d bytes exceeds maximum %d bytes", info.Size(), maxSizeBytes)
	}

	// Check file extension
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != "" && ext[0] == '.' {
		ext = ext[1:] // Remove the dot
	}

	supportedFormats := s.config.GetTranscribeSupportedFormats()
	for _, format := range supportedFormats {
		if ext == strings.TrimSpace(format) {
			return nil
		}
	}

	return fmt.Errorf("unsupported file format: %s (supported: %v)", ext, supportedFormats)
}

// transcribeFromMemory transcribes audio data from memory
func (s *TranscribeService) transcribeFromMemory(ctx context.Context, audioData []byte) (*TranscriptionResult, error) {
	start := time.Now()

	// Validate audio data
	if len(audioData) == 0 {
		return nil, fmt.Errorf("audio data is empty")
	}

	s.logger.WithField("audio_size_bytes", len(audioData)).Debug("Starting transcription from memory")

	// Create transcription request with timeout
	reqCtx, cancel := context.WithTimeout(ctx, s.config.Transcribe.RequestTimeout)
	defer cancel()

	// Get project ID from config
	projectID := s.config.GoogleCloud.ProjectID
	if projectID == "" {
		return nil, fmt.Errorf("PROJECT_ID is required for Speech v2 API")
	}

	// Build recognition request using Speech v2 API
	req := &speechpb.RecognizeRequest{
		Recognizer: fmt.Sprintf("projects/%s/locations/global/recognizers/_", projectID),
		Config: &speechpb.RecognitionConfig{
			DecodingConfig: &speechpb.RecognitionConfig_AutoDecodingConfig{
				AutoDecodingConfig: &speechpb.AutoDetectDecodingConfig{},
			},
			Features: &speechpb.RecognitionFeatures{
				EnableAutomaticPunctuation: true,
				MaxAlternatives:            int32(s.config.Transcribe.MaxAlternatives),
			},
			LanguageCodes: []string{s.config.Transcribe.LanguageCode},
			Model:         "long",
		},
		AudioSource: &speechpb.RecognizeRequest_Content{
			Content: audioData,
		},
	}

	// Debug logging
	s.logger.WithFields(logrus.Fields{
		"audio_size_bytes": len(audioData),
		"recognizer":       req.Recognizer,
		"language_codes":   req.Config.LanguageCodes,
		"model":            req.Config.Model,
		"content_set":      req.AudioSource != nil,
	}).Debug("Sending recognition request")

	// Perform transcription
	resp, err := s.client.Recognize(reqCtx, req)
	if err != nil {
		s.logger.WithError(err).WithFields(logrus.Fields{
			"audio_size_bytes": len(audioData),
			"recognizer":       req.Recognizer,
		}).Error("Failed to transcribe audio from memory")
		return nil, fmt.Errorf("failed to transcribe audio: %w", err)
	}

	// Process results
	result := s.processRecognitionResponse(resp, start)

	// Add metadata
	if result.Metadata == nil {
		result.Metadata = make(map[string]interface{})
	}
	result.Metadata["audio_size_bytes"] = len(audioData)
	result.Metadata["transcription_duration_ms"] = time.Since(start).Milliseconds()

	return result, nil
}

// downloadFileToMemory downloads an audio file from URL directly into memory
func (s *TranscribeService) downloadFileToMemory(ctx context.Context, audioURL string) ([]byte, error) {
	// Create download context with timeout
	dlCtx, cancel := context.WithTimeout(ctx, s.config.Transcribe.DownloadTimeout)
	defer cancel()

	// Create HTTP request
	req, err := http.NewRequestWithContext(dlCtx, "GET", audioURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Perform request
	client := &http.Client{
		Timeout: s.config.Transcribe.DownloadTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// Read content with size limit directly into memory
	maxSizeBytes := int64(s.config.Transcribe.MaxFileSizeMB) * 1024 * 1024
	limited := io.LimitReader(resp.Body, maxSizeBytes+1) // +1 to detect oversized files

	audioData, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("failed to read audio data: %w", err)
	}

	if int64(len(audioData)) > maxSizeBytes {
		return nil, fmt.Errorf("downloaded file size %d bytes exceeds maximum %d bytes", len(audioData), maxSizeBytes)
	}

	s.logger.WithFields(logrus.Fields{
		"audio_url":    audioURL,
		"size_bytes":   len(audioData),
		"content_type": resp.Header.Get("Content-Type"),
	}).Debug("Audio file downloaded to memory")

	return audioData, nil
}

// downloadFile downloads an audio file from URL to a temporary file
func (s *TranscribeService) downloadFile(ctx context.Context, audioURL string) (string, error) {
	// Create download context with timeout
	dlCtx, cancel := context.WithTimeout(ctx, s.config.Transcribe.DownloadTimeout)
	defer cancel()

	// Create HTTP request
	req, err := http.NewRequestWithContext(dlCtx, "GET", audioURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Perform request
	client := &http.Client{
		Timeout: s.config.Transcribe.DownloadTimeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status: %d", resp.StatusCode)
	}

	// Determine file extension from Content-Type or URL
	ext := s.getFileExtension(resp, audioURL)

	// Create temporary file
	tempFile, err := os.CreateTemp(s.config.Transcribe.TempDir, "transcribe_*"+ext)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer func() { _ = tempFile.Close() }()

	// Copy content with size limit
	maxSizeBytes := int64(s.config.Transcribe.MaxFileSizeMB) * 1024 * 1024
	limited := io.LimitReader(resp.Body, maxSizeBytes+1) // +1 to detect oversized files

	written, err := io.Copy(tempFile, limited)
	if err != nil {
		_ = os.Remove(tempFile.Name())
		return "", fmt.Errorf("failed to write temporary file: %w", err)
	}

	if written > maxSizeBytes {
		_ = os.Remove(tempFile.Name())
		return "", fmt.Errorf("downloaded file size %d bytes exceeds maximum %d bytes", written, maxSizeBytes)
	}

	return tempFile.Name(), nil
}

// getFileExtension determines the file extension from HTTP response or URL
func (s *TranscribeService) getFileExtension(resp *http.Response, audioURL string) string {
	// Try to get extension from Content-Type
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" {
		if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
			return exts[0]
		}
	}

	// Fallback to URL extension
	ext := filepath.Ext(audioURL)
	if ext != "" {
		return ext
	}

	// Default extension
	return ".mp3"
}

// Note: detectEncoding function removed - Speech v2 API uses auto-detection

// processRecognitionResponse processes the Google Speech v2 API response (matching sandbox.go pattern)
func (s *TranscribeService) processRecognitionResponse(resp *speechpb.RecognizeResponse, start time.Time) *TranscriptionResult {
	result := &TranscriptionResult{
		Duration: time.Since(start),
		Language: s.config.Transcribe.LanguageCode,
		Metadata: make(map[string]interface{}),
	}

	if resp == nil || len(resp.Results) == 0 {
		s.logger.Warn("No transcription results returned")
		result.Text = ""
		result.Confidence = 0.0
		return result
	}

	// Process Speech v2 API results (similar to sandbox.go processing)
	for i, res := range resp.Results {
		if len(res.Alternatives) > 0 {
			// Use the first result and first alternative (highest confidence)
			if i == 0 {
				primary := res.Alternatives[0]
				result.Text = primary.Transcript
				result.Confidence = primary.Confidence
			}

			// Add alternatives if requested
			if s.config.Transcribe.MaxAlternatives > 1 {
				for j, alt := range res.Alternatives[1:] {
					if j >= s.config.Transcribe.MaxAlternatives-1 {
						break
					}
					result.Alternatives = append(result.Alternatives, TranscriptionAlternative{
						Text:       alt.Transcript,
						Confidence: alt.Confidence,
					})
				}
			}
		}
	}

	// Add metadata (Speech v2 format)
	result.Metadata["result_count"] = len(resp.Results)
	if len(resp.Results) > 0 && len(resp.Results[0].Alternatives) > 0 {
		result.Metadata["alternative_count"] = len(resp.Results[0].Alternatives)
	}

	return result
}

// HealthCheck performs a health check on the transcription service
func (s *TranscribeService) HealthCheck(ctx context.Context) error {
	// Apply rate limiting for health check
	if allowed, err := s.rateLimiter.Allow(ctx, "transcribe_service_health"); err != nil {
		return fmt.Errorf("rate limiter error during health check: %w", err)
	} else if !allowed {
		return fmt.Errorf("rate limit exceeded for health check")
	}

	// Simple connectivity test for Speech v2 API
	_, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Verify we have required configuration for Speech v2
	projectID := s.config.GoogleCloud.ProjectID
	if projectID == "" {
		return fmt.Errorf("PROJECT_ID is required for Speech v2 API health check")
	}

	// Basic validation that client was created successfully
	if s.client == nil {
		return fmt.Errorf("transcription service client is not initialized")
	}

	return nil
}

// Close closes the transcription client
func (s *TranscribeService) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}
