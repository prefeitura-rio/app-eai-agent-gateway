package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// TempFileManager provides secure temporary file management with automatic cleanup
type TempFileManager struct {
	config        *config.Config
	logger        *logrus.Logger
	baseDir       string
	files         map[string]*TempFileInfo
	mu            sync.RWMutex
	cleanupTicker *time.Ticker
	stopCleanup   chan struct{}
}

// TempFileInfo tracks information about temporary files
type TempFileInfo struct {
	Path         string        `json:"path"`
	CreatedAt    time.Time     `json:"created_at"`
	Size         int64         `json:"size"`
	Purpose      string        `json:"purpose"`
	MaxAge       time.Duration `json:"max_age"`
	SecureDelete bool          `json:"secure_delete"`
	Cleaned      bool          `json:"cleaned"`
}

// TempFileConfig configures temporary file management
type TempFileConfig struct {
	BaseDir         string        `json:"base_dir"`
	MaxAge          time.Duration `json:"max_age"`
	MaxSize         int64         `json:"max_size"`
	CleanupInterval time.Duration `json:"cleanup_interval"`
	SecureDelete    bool          `json:"secure_delete"`
	AllowedDirs     []string      `json:"allowed_dirs"`
}

// NewTempFileManager creates a new temporary file manager
func NewTempFileManager(config *config.Config, logger *logrus.Logger) (*TempFileManager, error) {
	// Determine base directory for temporary files
	baseDir := os.TempDir()
	if config.Transcribe.TempDir != "" {
		baseDir = config.Transcribe.TempDir
	}

	// Create base directory if it doesn't exist
	if err := os.MkdirAll(baseDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	tfm := &TempFileManager{
		config:      config,
		logger:      logger,
		baseDir:     baseDir,
		files:       make(map[string]*TempFileInfo),
		stopCleanup: make(chan struct{}),
	}

	// Start cleanup routine
	tfm.startCleanupRoutine()

	return tfm, nil
}

// CreateTempFile creates a temporary file with specified parameters
func (tfm *TempFileManager) CreateTempFile(purpose, extension string, maxAge time.Duration) (*TempFileInfo, error) {
	tfm.mu.Lock()
	defer tfm.mu.Unlock()

	// Generate secure temporary file name
	filename := fmt.Sprintf("eai_temp_%d_%s", time.Now().UnixNano(), generateSecureID())
	if extension != "" {
		if !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		filename += extension
	}

	// Ensure file is created in our base directory
	filePath := filepath.Join(tfm.baseDir, filename)

	// Validate path security
	if err := tfm.validatePath(filePath); err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	// Create the file
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	_ = file.Close()

	// Set default max age if not specified
	if maxAge == 0 {
		maxAge = 30 * time.Minute // Default 30 minutes
	}

	info := &TempFileInfo{
		Path:         filePath,
		CreatedAt:    time.Now(),
		Size:         0,
		Purpose:      purpose,
		MaxAge:       maxAge,
		SecureDelete: true,
		Cleaned:      false,
	}

	tfm.files[filePath] = info

	tfm.logger.WithFields(logrus.Fields{
		"path":    filePath,
		"purpose": purpose,
		"max_age": maxAge,
	}).Debug("Created temporary file")

	return info, nil
}

// TrackExistingFile tracks an existing file for cleanup
func (tfm *TempFileManager) TrackExistingFile(filePath, purpose string, maxAge time.Duration) (*TempFileInfo, error) {
	tfm.mu.Lock()
	defer tfm.mu.Unlock()

	// Validate path security
	if err := tfm.validatePath(filePath); err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	// Get file info
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	// Set default max age if not specified
	if maxAge == 0 {
		maxAge = 30 * time.Minute
	}

	info := &TempFileInfo{
		Path:         filePath,
		CreatedAt:    stat.ModTime(),
		Size:         stat.Size(),
		Purpose:      purpose,
		MaxAge:       maxAge,
		SecureDelete: true,
		Cleaned:      false,
	}

	tfm.files[filePath] = info

	tfm.logger.WithFields(logrus.Fields{
		"path":    filePath,
		"purpose": purpose,
		"size":    stat.Size(),
		"max_age": maxAge,
	}).Debug("Tracking existing temporary file")

	return info, nil
}

// CleanupFile immediately cleans up a specific file
func (tfm *TempFileManager) CleanupFile(filePath string) error {
	tfm.mu.Lock()
	defer tfm.mu.Unlock()

	info, exists := tfm.files[filePath]
	if !exists {
		return fmt.Errorf("file not tracked: %s", filePath)
	}

	return tfm.cleanupFileUnsafe(info)
}

// CleanupAll immediately cleans up all tracked files
func (tfm *TempFileManager) CleanupAll() error {
	tfm.mu.Lock()
	defer tfm.mu.Unlock()

	var errors []string

	for _, info := range tfm.files {
		if !info.Cleaned {
			if err := tfm.cleanupFileUnsafe(info); err != nil {
				errors = append(errors, err.Error())
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errors, "; "))
	}

	return nil
}

// GetTrackedFiles returns information about all tracked files
func (tfm *TempFileManager) GetTrackedFiles() []*TempFileInfo {
	tfm.mu.RLock()
	defer tfm.mu.RUnlock()

	files := make([]*TempFileInfo, 0, len(tfm.files))
	for _, info := range tfm.files {
		// Create a copy to avoid race conditions
		infoCopy := *info
		files = append(files, &infoCopy)
	}

	return files
}

// GetFileInfo returns information about a specific tracked file
func (tfm *TempFileManager) GetFileInfo(filePath string) (*TempFileInfo, bool) {
	tfm.mu.RLock()
	defer tfm.mu.RUnlock()

	info, exists := tfm.files[filePath]
	if !exists {
		return nil, false
	}

	// Return a copy
	infoCopy := *info
	return &infoCopy, true
}

// UpdateFileSize updates the size of a tracked file
func (tfm *TempFileManager) UpdateFileSize(filePath string) error {
	tfm.mu.Lock()
	defer tfm.mu.Unlock()

	info, exists := tfm.files[filePath]
	if !exists {
		return fmt.Errorf("file not tracked: %s", filePath)
	}

	stat, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	info.Size = stat.Size()
	return nil
}

// Close stops the cleanup routine and cleans up all files
func (tfm *TempFileManager) Close() error {
	// Stop cleanup routine
	if tfm.cleanupTicker != nil {
		tfm.cleanupTicker.Stop()
	}
	close(tfm.stopCleanup)

	// Clean up all remaining files
	return tfm.CleanupAll()
}

// HealthCheck performs a health check for the temp file manager
func (tfm *TempFileManager) HealthCheck(ctx context.Context) error {
	// Check if base directory is accessible
	if _, err := os.Stat(tfm.baseDir); err != nil {
		return fmt.Errorf("base directory not accessible: %w", err)
	}

	// Check if we can create a test file
	testFile, err := tfm.CreateTempFile("health_check", ".test", 1*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to create test file: %w", err)
	}

	// Clean up test file
	if err := tfm.CleanupFile(testFile.Path); err != nil {
		return fmt.Errorf("failed to cleanup test file: %w", err)
	}

	return nil
}

// Private methods

func (tfm *TempFileManager) startCleanupRoutine() {
	interval := 5 * time.Minute // Default cleanup interval
	if tfm.config.Transcribe.CleanupInterval > 0 {
		interval = tfm.config.Transcribe.CleanupInterval
	}

	tfm.cleanupTicker = time.NewTicker(interval)

	go func() {
		defer tfm.cleanupTicker.Stop()

		for {
			select {
			case <-tfm.cleanupTicker.C:
				tfm.performScheduledCleanup()
			case <-tfm.stopCleanup:
				return
			}
		}
	}()
}

func (tfm *TempFileManager) performScheduledCleanup() {
	tfm.mu.Lock()
	defer tfm.mu.Unlock()

	now := time.Now()
	var toCleanup []*TempFileInfo

	// Find files that need cleanup
	for _, info := range tfm.files {
		if !info.Cleaned && now.Sub(info.CreatedAt) > info.MaxAge {
			toCleanup = append(toCleanup, info)
		}
	}

	if len(toCleanup) == 0 {
		return
	}

	tfm.logger.WithField("count", len(toCleanup)).Info("Starting scheduled temp file cleanup")

	// Clean up expired files
	for _, info := range toCleanup {
		if err := tfm.cleanupFileUnsafe(info); err != nil {
			tfm.logger.WithError(err).WithField("path", info.Path).Error("Failed to cleanup temp file")
		}
	}
}

func (tfm *TempFileManager) cleanupFileUnsafe(info *TempFileInfo) error {
	if info.Cleaned {
		return nil // Already cleaned
	}

	// Check if file still exists
	if _, err := os.Stat(info.Path); os.IsNotExist(err) {
		info.Cleaned = true
		tfm.logger.WithField("path", info.Path).Debug("Temp file already removed")
		return nil
	}

	// Perform secure deletion if enabled
	if info.SecureDelete {
		if err := tfm.secureDeleteFile(info.Path); err != nil {
			tfm.logger.WithError(err).WithField("path", info.Path).Warn("Secure delete failed, falling back to normal delete")
		}
	}

	// Normal file deletion
	if err := os.Remove(info.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove file %s: %w", info.Path, err)
	}

	info.Cleaned = true

	tfm.logger.WithFields(logrus.Fields{
		"path":          info.Path,
		"purpose":       info.Purpose,
		"size":          info.Size,
		"age":           time.Since(info.CreatedAt),
		"secure_delete": info.SecureDelete,
	}).Debug("Cleaned up temporary file")

	return nil
}

func (tfm *TempFileManager) secureDeleteFile(filePath string) error {
	// Get file size
	stat, err := os.Stat(filePath)
	if err != nil {
		return err
	}

	// Open file for writing
	file, err := os.OpenFile(filePath, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	// Overwrite with zeros (simple secure delete)
	zeros := make([]byte, 4096)
	remaining := stat.Size()

	for remaining > 0 {
		writeSize := int64(len(zeros))
		if remaining < writeSize {
			writeSize = remaining
		}

		if _, err := file.Write(zeros[:writeSize]); err != nil {
			return err
		}

		remaining -= writeSize
	}

	// Sync to disk
	if err := file.Sync(); err != nil {
		return err
	}

	return nil
}

func (tfm *TempFileManager) validatePath(filePath string) error {
	// Resolve any symbolic links and get absolute path
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("failed to resolve path: %w", err)
	}

	// Check if path is within our base directory
	baseAbs, err := filepath.Abs(tfm.baseDir)
	if err != nil {
		return fmt.Errorf("failed to resolve base directory: %w", err)
	}

	if !strings.HasPrefix(absPath, baseAbs) {
		return fmt.Errorf("path outside of allowed directory: %s", absPath)
	}

	// Check for path traversal attempts
	if strings.Contains(filePath, "..") {
		return fmt.Errorf("path traversal detected: %s", filePath)
	}

	// Check for suspicious characters
	if strings.ContainsAny(filePath, "<>:\"|?*") {
		return fmt.Errorf("invalid characters in path: %s", filePath)
	}

	return nil
}

func generateSecureID() string {
	// Simple implementation - in production might want to use crypto/rand
	return fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
}
