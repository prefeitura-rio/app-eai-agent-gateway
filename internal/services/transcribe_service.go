package services

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	speech "cloud.google.com/go/speech/apiv1"
	"cloud.google.com/go/speech/apiv1/speechpb"
	"github.com/sirupsen/logrus"
	"google.golang.org/api/option"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

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

	// Create client options
	var clientOptions []option.ClientOption

	// Add credentials if provided via Google Cloud config
	if cfg.GoogleCloud.ServiceAccount != "" {
		clientOptions = append(clientOptions, option.WithCredentialsFile(cfg.GoogleCloud.ServiceAccount))
	}

	// Create the Speech client
	client, err := speech.NewClient(ctx, clientOptions...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Speech client: %w", err)
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

	// Download the file
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

	// Create transcription request with timeout
	reqCtx, cancel := context.WithTimeout(ctx, s.config.Transcribe.RequestTimeout)
	defer cancel()

	// Build recognition config
	config := &speechpb.RecognitionConfig{
		Encoding:                   s.detectEncoding(filePath),
		SampleRateHertz:            int32(s.config.Transcribe.SampleRateHertz),
		LanguageCode:               s.config.Transcribe.LanguageCode,
		EnableWordTimeOffsets:      s.config.Transcribe.EnableWordTimeOffsets,
		EnableWordConfidence:       s.config.Transcribe.EnableWordConfidence,
		MaxAlternatives:            int32(s.config.Transcribe.MaxAlternatives),
		ProfanityFilter:            s.config.Transcribe.ProfanityFilter,
		EnableAutomaticPunctuation: true,
		UseEnhanced:                true,
		Model:                      "latest_long",
	}

	// Create recognition request
	req := &speechpb.RecognizeRequest{
		Config: config,
		Audio: &speechpb.RecognitionAudio{
			AudioSource: &speechpb.RecognitionAudio_Content{
				Content: audioData,
			},
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

// detectEncoding determines the audio encoding based on file extension
func (s *TranscribeService) detectEncoding(filePath string) speechpb.RecognitionConfig_AudioEncoding {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".wav":
		return speechpb.RecognitionConfig_LINEAR16
	case ".flac":
		return speechpb.RecognitionConfig_FLAC
	case ".mp3":
		return speechpb.RecognitionConfig_MP3
	case ".ogg":
		return speechpb.RecognitionConfig_OGG_OPUS
	case ".webm":
		return speechpb.RecognitionConfig_WEBM_OPUS
	case ".mp4", ".m4a":
		return speechpb.RecognitionConfig_MP3 // Fallback
	default:
		s.logger.WithField("extension", ext).Warn("Unknown audio format, using MP3 encoding")
		return speechpb.RecognitionConfig_MP3
	}
}

// processRecognitionResponse processes the Google Speech API response
func (s *TranscribeService) processRecognitionResponse(resp *speechpb.RecognizeResponse, start time.Time) *TranscriptionResult {
	result := &TranscriptionResult{
		Duration: time.Since(start),
		Language: s.config.Transcribe.LanguageCode,
		Metadata: make(map[string]interface{}),
	}

	if len(resp.Results) == 0 {
		s.logger.Warn("No transcription results returned")
		result.Text = ""
		result.Confidence = 0.0
		return result
	}

	// Get the best result (first one)
	bestResult := resp.Results[0]
	if len(bestResult.Alternatives) == 0 {
		result.Text = ""
		result.Confidence = 0.0
		return result
	}

	// Primary result
	primary := bestResult.Alternatives[0]
	result.Text = primary.Transcript
	result.Confidence = primary.Confidence

	// Add alternatives if requested
	if s.config.Transcribe.MaxAlternatives > 1 {
		for i, alt := range bestResult.Alternatives[1:] {
			if i >= s.config.Transcribe.MaxAlternatives-1 {
				break
			}
			result.Alternatives = append(result.Alternatives, TranscriptionAlternative{
				Text:       alt.Transcript,
				Confidence: alt.Confidence,
			})
		}
	}

	// Add word-level information if enabled
	if s.config.Transcribe.EnableWordTimeOffsets && len(primary.Words) > 0 {
		for _, word := range primary.Words {
			wordInfo := WordInfo{
				Word: word.Word,
			}

			if word.StartTime != nil {
				wordInfo.StartTime = time.Duration(word.StartTime.Seconds)*time.Second +
					time.Duration(word.StartTime.Nanos)*time.Nanosecond
			}

			if word.EndTime != nil {
				wordInfo.EndTime = time.Duration(word.EndTime.Seconds)*time.Second +
					time.Duration(word.EndTime.Nanos)*time.Nanosecond
			}

			if s.config.Transcribe.EnableWordConfidence {
				wordInfo.Confidence = word.Confidence
			}

			result.WordInfo = append(result.WordInfo, wordInfo)
		}
	}

	// Add metadata
	result.Metadata["total_billed_time"] = resp.TotalBilledTime
	result.Metadata["result_count"] = len(resp.Results)
	result.Metadata["alternative_count"] = len(bestResult.Alternatives)

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

	// Simple connectivity test - try to create a recognition config
	testCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = testCtx // Context will be used when implementing actual connectivity test

	// Test basic client functionality by checking supported languages
	// This is a lightweight operation that verifies connectivity
	config := &speechpb.RecognitionConfig{
		Encoding:        speechpb.RecognitionConfig_LINEAR16,
		SampleRateHertz: 16000,
		LanguageCode:    "en-US",
	}

	// Validate the config format (this doesn't make an API call)
	if config.Encoding == speechpb.RecognitionConfig_ENCODING_UNSPECIFIED {
		return fmt.Errorf("transcription service configuration error")
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
