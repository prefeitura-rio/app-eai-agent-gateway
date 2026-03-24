package services

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "github.com/lib/pq" // PostgreSQL driver
	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// PostgresService handles PostgreSQL operations for agent engine database
type PostgresService struct {
	db     *sql.DB
	logger *logrus.Logger
	config *config.Config
}

// NewPostgresService creates a new PostgreSQL service with connection pooling
func NewPostgresService(cfg *config.Config, logger *logrus.Logger) (*PostgresService, error) {
	// Open connection with PostgreSQL driver
	db, err := sql.Open("postgres", cfg.Postgres.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open postgres connection: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(cfg.Postgres.MaxOpenConns)
	db.SetMaxIdleConns(cfg.Postgres.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.Postgres.ConnMaxLifetime)

	// Test connection with retry logic
	const maxRetries = 5
	const baseBackoff = 2 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := db.PingContext(ctx)
		cancel()

		if err == nil {
			break
		}

		lastErr = err

		if attempt == maxRetries {
			db.Close()
			return nil, fmt.Errorf("failed to connect to postgres after %d attempts: %w", maxRetries, lastErr)
		}

		backoff := time.Duration(attempt) * baseBackoff
		logger.WithFields(logrus.Fields{
			"attempt":     attempt,
			"max_retries": maxRetries,
			"backoff":     backoff,
			"error":       err,
		}).Warn("Postgres connection attempt failed, retrying...")

		time.Sleep(backoff)
	}

	logger.WithFields(logrus.Fields{
		"max_open_conns": cfg.Postgres.MaxOpenConns,
		"max_idle_conns": cfg.Postgres.MaxIdleConns,
		"max_lifetime":   cfg.Postgres.ConnMaxLifetime,
	}).Info("Postgres service initialized successfully")

	return &PostgresService{
		db:     db,
		logger: logger,
		config: cfg,
	}, nil
}

// GetLastMessageTimestamp retrieves the timestamp of the most recent message from a user
// Uses parameterized query ($1 placeholder) to prevent SQL injection
func (p *PostgresService) GetLastMessageTimestamp(ctx context.Context, threadID string) (*time.Time, error) {
	// Use $1 placeholder for parameterized query - prevents SQL injection
	query := `
		SELECT c.checkpoint_id
		FROM checkpoints c
		WHERE c.thread_id = $1
		  AND c.checkpoint_ns = ''
		ORDER BY c.checkpoint_id DESC
		LIMIT 1
	`

	var checkpointID string

	// QueryRowContext with parameterized query - threadID is safely escaped by driver
	err := p.db.QueryRowContext(ctx, query, threadID).Scan(&checkpointID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no messages found for thread: %s", threadID)
		}
		p.logger.WithError(err).WithField("thread_id", threadID).Error("Failed to query last message timestamp")
		return nil, fmt.Errorf("postgres query error: %w", err)
	}

	// Extract timestamp from UUID v7 checkpoint_id
	// UUID v7 format: first 48 bits are Unix timestamp in milliseconds
	// Format: "1f11c8ec-ce30-6641-8017-8909ed283a09"
	timestamp, err := extractTimestampFromUUIDv7(checkpointID)
	if err != nil {
		p.logger.WithError(err).WithFields(logrus.Fields{
			"thread_id":     threadID,
			"checkpoint_id": checkpointID,
		}).Error("Failed to extract timestamp from UUID v7")
		return nil, fmt.Errorf("invalid checkpoint_id format: %w", err)
	}

	p.logger.WithFields(logrus.Fields{
		"thread_id":     threadID,
		"checkpoint_id": checkpointID,
		"timestamp":     timestamp,
	}).Debug("Retrieved last message timestamp from postgres")

	return timestamp, nil
}

// extractTimestampFromUUIDv7 extracts the Unix timestamp from a UUID v7 string
// UUID v7 format embeds a 48-bit Unix timestamp (milliseconds) in the first segment
func extractTimestampFromUUIDv7(uuidStr string) (*time.Time, error) {
	// Remove hyphens from UUID: "1f11c8ec-ce30-6641-8017-8909ed283a09" -> "1f11c8ecce3066418017890"
	cleaned := ""
	for _, c := range uuidStr {
		if c != '-' {
			cleaned += string(c)
		}
	}

	// First 12 hex chars (48 bits) represent timestamp in milliseconds
	if len(cleaned) < 12 {
		return nil, fmt.Errorf("UUID too short: %s", uuidStr)
	}

	timestampHex := cleaned[:12]

	// Decode hex to bytes
	timestampBytes, err := hex.DecodeString(timestampHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode timestamp hex: %w", err)
	}

	// Convert bytes to milliseconds (48-bit big-endian integer)
	var timestampMillis int64
	for i := 0; i < len(timestampBytes); i++ {
		timestampMillis = (timestampMillis << 8) | int64(timestampBytes[i])
	}

	// Convert milliseconds to time.Time
	timestamp := time.UnixMilli(timestampMillis)

	return &timestamp, nil
}

// GetThreadMessageCount returns the total number of checkpoints for a thread
// Uses parameterized query ($1 placeholder) to prevent SQL injection
func (p *PostgresService) GetThreadMessageCount(ctx context.Context, threadID string) (int64, error) {
	query := `
		SELECT COUNT(*)
		FROM checkpoints
		WHERE thread_id = $1
		  AND checkpoint_ns = ''
	`

	var count int64

	err := p.db.QueryRowContext(ctx, query, threadID).Scan(&count)
	if err != nil {
		p.logger.WithError(err).WithField("thread_id", threadID).Error("Failed to count thread messages")
		return 0, fmt.Errorf("postgres query error: %w", err)
	}

	return count, nil
}

// HealthCheck tests the PostgreSQL connection
func (p *PostgresService) HealthCheck(ctx context.Context) error {
	if err := p.db.PingContext(ctx); err != nil {
		p.logger.WithError(err).Error("Postgres ping failed")
		return fmt.Errorf("postgres ping error: %w", err)
	}
	return nil
}

// Close closes the PostgreSQL connection
func (p *PostgresService) Close() error {
	if err := p.db.Close(); err != nil {
		p.logger.WithError(err).Error("Failed to close Postgres connection")
		return fmt.Errorf("postgres close error: %w", err)
	}
	p.logger.Info("Postgres connection closed")
	return nil
}

// GetStats returns PostgreSQL connection pool statistics
func (p *PostgresService) GetStats() sql.DBStats {
	return p.db.Stats()
}
