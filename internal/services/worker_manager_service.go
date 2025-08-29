package services

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/workers"
)

// WorkerManagerService implements worker lifecycle management
type WorkerManagerService struct {
	config *config.Config
	logger *logrus.Logger

	// Worker management
	workers map[models.WorkerType]workers.Worker
	metrics workers.WorkerMetrics
	deps    *workers.WorkerDependencies

	// Dependencies for worker creation
	rabbitMQ    workers.RabbitMQServiceInterface
	consumerMgr workers.ConsumerManagerInterface

	// Synchronization
	mu      sync.RWMutex
	running bool

	// Cancellation
	ctx    context.Context
	cancel context.CancelFunc

	// For testing - allow overriding worker creation
	workerFactory func(models.WorkerType) (workers.Worker, error)
}

// NewWorkerManagerService creates a new worker manager service
func NewWorkerManagerService(cfg *config.Config, logger *logrus.Logger, deps *workers.WorkerDependencies, rabbitMQ workers.RabbitMQServiceInterface, consumerMgr workers.ConsumerManagerInterface) *WorkerManagerService {
	ctx, cancel := context.WithCancel(context.Background())

	service := &WorkerManagerService{
		config:      cfg,
		logger:      logger,
		workers:     make(map[models.WorkerType]workers.Worker),
		deps:        deps,
		rabbitMQ:    rabbitMQ,
		consumerMgr: consumerMgr,
		ctx:         ctx,
		cancel:      cancel,
		running:     false,
	}

	// Use the metrics from dependencies if provided, otherwise create a default one
	if deps != nil && deps.Metrics != nil {
		service.metrics = deps.Metrics
	} else {
		service.metrics = NewWorkerMetricsService(logger)
	}

	// Set default worker factory
	service.workerFactory = service.createWorker

	logger.WithFields(logrus.Fields{
		"max_parallel": cfg.MaxParallel,
	}).Info("Worker manager service initialized")

	return service
}

// StartWorker starts a specific worker type with the given concurrency
func (w *WorkerManagerService) StartWorker(ctx context.Context, workerType models.WorkerType, concurrency int) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Check if worker is already running
	if existingWorker, exists := w.workers[workerType]; exists {
		if existingWorker.IsRunning() {
			return fmt.Errorf("worker %s is already running", workerType)
		}
		// Stop existing worker first
		if err := existingWorker.Stop(ctx); err != nil {
			w.logger.WithError(err).WithField("worker_type", workerType).Warn("Failed to stop existing worker")
		}
	}

	// Create the appropriate worker instance
	worker, err := w.workerFactory(workerType)
	if err != nil {
		return fmt.Errorf("failed to create worker %s: %w", workerType, err)
	}

	// Store the worker
	w.workers[workerType] = worker

	// Start the worker in a goroutine
	go func() {
		w.logger.WithFields(logrus.Fields{
			"worker_type": workerType,
			"concurrency": concurrency,
		}).Info("Starting worker")

		if err := worker.Start(w.ctx); err != nil {
			w.logger.WithError(err).WithField("worker_type", workerType).Error("Worker failed to start")
			w.metrics.RecordWorkerHealth(workerType, false)
		}
	}()

	// Wait a moment to ensure the worker has started
	time.Sleep(100 * time.Millisecond)

	// Record worker health
	w.metrics.RecordWorkerHealth(workerType, worker.IsRunning())

	w.logger.WithField("worker_type", workerType).Info("Worker started successfully")
	return nil
}

// StopWorker stops a specific worker type
func (w *WorkerManagerService) StopWorker(ctx context.Context, workerType models.WorkerType) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	worker, exists := w.workers[workerType]
	if !exists {
		return fmt.Errorf("worker %s is not registered", workerType)
	}

	if !worker.IsRunning() {
		w.logger.WithField("worker_type", workerType).Debug("Worker is already stopped")
		return nil
	}

	w.logger.WithField("worker_type", workerType).Info("Stopping worker")

	// Create a timeout context for stopping
	stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
	defer stopCancel()

	if err := worker.Stop(stopCtx); err != nil {
		w.logger.WithError(err).WithField("worker_type", workerType).Error("Failed to stop worker gracefully")
		w.metrics.RecordWorkerHealth(workerType, false)
		return fmt.Errorf("failed to stop worker %s: %w", workerType, err)
	}

	// Remove from workers map
	delete(w.workers, workerType)

	// Record worker health
	w.metrics.RecordWorkerHealth(workerType, false)

	w.logger.WithField("worker_type", workerType).Info("Worker stopped successfully")
	return nil
}

// StopAll stops all workers gracefully
func (w *WorkerManagerService) StopAll(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.running {
		w.logger.Debug("Worker manager is already stopped")
		return nil
	}

	w.logger.Info("Stopping all workers")

	var errors []error
	var wg sync.WaitGroup

	// Stop all workers concurrently
	for workerType, worker := range w.workers {
		if !worker.IsRunning() {
			continue
		}

		wg.Add(1)
		go func(wt models.WorkerType, wk workers.Worker) {
			defer wg.Done()

			// Create a timeout context for each worker
			stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
			defer stopCancel()

			w.logger.WithField("worker_type", wt).Info("Stopping worker")

			if err := wk.Stop(stopCtx); err != nil {
				w.logger.WithError(err).WithField("worker_type", wt).Error("Failed to stop worker")
				w.mu.Lock()
				errors = append(errors, fmt.Errorf("failed to stop worker %s: %w", wt, err))
				w.mu.Unlock()
			} else {
				w.logger.WithField("worker_type", wt).Info("Worker stopped successfully")
			}

			// Record worker health
			w.metrics.RecordWorkerHealth(wt, false)
		}(workerType, worker)
	}

	// Wait for all workers to stop
	wg.Wait()

	// Clear workers map
	w.workers = make(map[models.WorkerType]workers.Worker)

	// Cancel the main context
	w.cancel()
	w.running = false

	if len(errors) > 0 {
		return fmt.Errorf("errors stopping workers: %v", errors)
	}

	w.logger.Info("All workers stopped successfully")
	return nil
}

// GetWorkerStatus returns the status of all workers
func (w *WorkerManagerService) GetWorkerStatus() map[models.WorkerType]workers.WorkerStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()

	status := make(map[models.WorkerType]workers.WorkerStatus)

	for workerType, worker := range w.workers {
		now := time.Now()
		workerStatus := workers.WorkerStatus{
			Type:        workerType,
			Running:     worker.IsRunning(),
			Concurrency: 1, // Default concurrency
			QueueName:   worker.GetQueueName(),
			LastSeen:    &now,
		}

		// Set started time if running
		if worker.IsRunning() {
			workerStatus.StartedAt = &now
		}

		status[workerType] = workerStatus
	}

	return status
}

// GetMetrics returns worker metrics
func (w *WorkerManagerService) GetMetrics() workers.WorkerMetrics {
	return w.metrics
}

// IsRunning returns whether the worker manager is running
func (w *WorkerManagerService) IsRunning() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.running
}

// Start starts the worker manager and all configured workers
func (w *WorkerManagerService) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.running {
		return fmt.Errorf("worker manager is already running")
	}

	w.logger.Info("Starting worker manager")

	// Start all configured worker types
	workerTypes := []models.WorkerType{
		models.WorkerTypeUserMessage,
	}

	for _, workerType := range workerTypes {
		worker, err := w.workerFactory(workerType)
		if err != nil {
			w.logger.WithError(err).WithField("worker_type", workerType).Error("Failed to create worker")
			continue
		}

		w.workers[workerType] = worker

		// Start worker in goroutine
		go func(wt models.WorkerType, wk workers.Worker) {
			w.logger.WithField("worker_type", wt).Info("Starting worker")

			if err := wk.Start(w.ctx); err != nil {
				w.logger.WithError(err).WithField("worker_type", wt).Error("Worker failed")
				w.metrics.RecordWorkerHealth(wt, false)
			}
		}(workerType, worker)

		// Record initial health
		w.metrics.RecordWorkerHealth(workerType, true)
	}

	w.running = true
	w.logger.Info("Worker manager started successfully")
	return nil
}

// Stop stops the worker manager and all workers
func (w *WorkerManagerService) Stop(ctx context.Context) error {
	return w.StopAll(ctx)
}

// RestartWorker restarts a specific worker
func (w *WorkerManagerService) RestartWorker(ctx context.Context, workerType models.WorkerType) error {
	w.logger.WithField("worker_type", workerType).Info("Restarting worker")

	// Stop the worker first
	if err := w.StopWorker(ctx, workerType); err != nil {
		w.logger.WithError(err).WithField("worker_type", workerType).Warn("Failed to stop worker during restart")
	}

	// Wait a moment before restarting
	time.Sleep(500 * time.Millisecond)

	// Start the worker again
	if err := w.StartWorker(ctx, workerType, 1); err != nil {
		return fmt.Errorf("failed to restart worker %s: %w", workerType, err)
	}

	w.logger.WithField("worker_type", workerType).Info("Worker restarted successfully")
	return nil
}

// HealthCheck performs a health check on all workers
func (w *WorkerManagerService) HealthCheck(ctx context.Context) error {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if !w.running {
		return fmt.Errorf("worker manager is not running")
	}

	var unhealthyWorkers []models.WorkerType

	for workerType, worker := range w.workers {
		if !worker.IsRunning() {
			unhealthyWorkers = append(unhealthyWorkers, workerType)
			w.metrics.RecordWorkerHealth(workerType, false)
		} else {
			w.metrics.RecordWorkerHealth(workerType, true)
		}
	}

	if len(unhealthyWorkers) > 0 {
		return fmt.Errorf("unhealthy workers: %v", unhealthyWorkers)
	}

	return nil
}

// GetWorkerCount returns the number of registered workers
func (w *WorkerManagerService) GetWorkerCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.workers)
}

// GetRunningWorkerCount returns the number of running workers
func (w *WorkerManagerService) GetRunningWorkerCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()

	count := 0
	for _, worker := range w.workers {
		if worker.IsRunning() {
			count++
		}
	}
	return count
}

// createWorker creates a worker instance of the specified type
func (w *WorkerManagerService) createWorker(workerType models.WorkerType) (workers.Worker, error) {
	switch workerType {
	case models.WorkerTypeUserMessage:
		return workers.NewUserMessageWorker(1, w.deps, w.rabbitMQ, w.consumerMgr), nil
	default:
		return nil, fmt.Errorf("unknown worker type: %s", workerType)
	}
}

// MonitorWorkers starts monitoring worker health and automatically restarts failed workers
func (w *WorkerManagerService) MonitorWorkers(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	w.logger.Info("Starting worker health monitoring")

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("Stopping worker health monitoring")
			return
		case <-ticker.C:
			w.performHealthCheck()
		}
	}
}

// performHealthCheck checks worker health and restarts failed workers
func (w *WorkerManagerService) performHealthCheck() {
	w.mu.RLock()
	workersToRestart := make([]models.WorkerType, 0)

	for workerType, worker := range w.workers {
		if !worker.IsRunning() {
			w.logger.WithField("worker_type", workerType).Warn("Worker is not running, scheduling restart")
			workersToRestart = append(workersToRestart, workerType)
			w.metrics.RecordWorkerHealth(workerType, false)
		} else {
			w.metrics.RecordWorkerHealth(workerType, true)
		}
	}
	w.mu.RUnlock()

	// Restart failed workers
	for _, workerType := range workersToRestart {
		restartCtx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		if err := w.RestartWorker(restartCtx, workerType); err != nil {
			w.logger.WithError(err).WithField("worker_type", workerType).Error("Failed to restart worker during health check")
		}
		cancel()
	}
}
