package services

import (
	"context"
	"database/sql"
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
	// Extract timestamp from checkpoint->>'ts' field
	// LangGraph stores timestamp in ISO 8601 format in the 'ts' field of the checkpoint JSONB column
	query := `
		SELECT checkpoint->>'ts' as timestamp
		FROM checkpoints
		WHERE thread_id = $1
		  AND checkpoint_ns = ''
		  AND checkpoint->>'ts' IS NOT NULL
		ORDER BY checkpoint_id DESC
		LIMIT 1
	`

	var timestampStr string

	// QueryRowContext with parameterized query - threadID is safely escaped by driver
	err := p.db.QueryRowContext(ctx, query, threadID).Scan(&timestampStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("no messages found for thread: %s", threadID)
		}
		p.logger.WithError(err).WithField("thread_id", threadID).Error("Failed to query last message timestamp")
		return nil, fmt.Errorf("postgres query error: %w", err)
	}

	// Parse ISO 8601 timestamp from JSONB
	timestamp, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		p.logger.WithError(err).WithFields(logrus.Fields{
			"thread_id":     threadID,
			"timestamp_str": timestampStr,
		}).Error("Failed to parse timestamp")
		return nil, fmt.Errorf("invalid timestamp format: %w", err)
	}

	p.logger.WithFields(logrus.Fields{
		"thread_id": threadID,
		"timestamp": timestamp,
	}).Debug("Retrieved last message timestamp from postgres")

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
