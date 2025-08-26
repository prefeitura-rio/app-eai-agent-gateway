package services

import (
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/models"
)

// WorkerMetricsService implements worker metrics collection and monitoring
type WorkerMetricsService struct {
	logger *logrus.Logger

	// Metrics storage
	mu                    sync.RWMutex
	messageProcessedCount map[models.WorkerType]map[string]int64 // [workerType][success/failure] -> count
	messageProcessingTime map[models.WorkerType][]time.Duration  // Processing duration history
	queueDepth            map[string]int                         // [queueName] -> depth
	workerHealth          map[models.WorkerType]bool             // [workerType] -> healthy

	// Statistics
	totalMessages         int64
	totalSuccessful       int64
	totalFailed           int64
	averageProcessingTime map[models.WorkerType]time.Duration

	// Monitoring
	startTime time.Time
}

// NewWorkerMetricsService creates a new worker metrics service
func NewWorkerMetricsService(logger *logrus.Logger) *WorkerMetricsService {
	return &WorkerMetricsService{
		logger:                logger,
		messageProcessedCount: make(map[models.WorkerType]map[string]int64),
		messageProcessingTime: make(map[models.WorkerType][]time.Duration),
		queueDepth:            make(map[string]int),
		workerHealth:          make(map[models.WorkerType]bool),
		averageProcessingTime: make(map[models.WorkerType]time.Duration),
		startTime:             time.Now(),
	}
}

// RecordMessageProcessed increments the count of processed messages
func (w *WorkerMetricsService) RecordMessageProcessed(workerType models.WorkerType, success bool, duration time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Initialize maps if needed
	if w.messageProcessedCount[workerType] == nil {
		w.messageProcessedCount[workerType] = make(map[string]int64)
	}
	if w.messageProcessingTime[workerType] == nil {
		w.messageProcessingTime[workerType] = make([]time.Duration, 0)
	}

	// Record count
	status := "failure"
	if success {
		status = "success"
		w.totalSuccessful++
	} else {
		w.totalFailed++
	}

	w.messageProcessedCount[workerType][status]++
	w.totalMessages++

	// Record processing time
	w.messageProcessingTime[workerType] = append(w.messageProcessingTime[workerType], duration)

	// Keep only last 1000 processing times to avoid memory growth
	if len(w.messageProcessingTime[workerType]) > 1000 {
		w.messageProcessingTime[workerType] = w.messageProcessingTime[workerType][len(w.messageProcessingTime[workerType])-1000:]
	}

	// Update average processing time
	w.updateAverageProcessingTime(workerType)

	w.logger.WithFields(logrus.Fields{
		"worker_type": workerType,
		"success":     success,
		"duration_ms": duration.Milliseconds(),
	}).Debug("Recorded message processing metrics")
}

// RecordQueueDepth records the current queue depth
func (w *WorkerMetricsService) RecordQueueDepth(queueName string, depth int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.queueDepth[queueName] = depth

	w.logger.WithFields(logrus.Fields{
		"queue_name": queueName,
		"depth":      depth,
	}).Debug("Recorded queue depth")
}

// RecordWorkerHealth records worker health status
func (w *WorkerMetricsService) RecordWorkerHealth(workerType models.WorkerType, healthy bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.workerHealth[workerType] = healthy

	w.logger.WithFields(logrus.Fields{
		"worker_type": workerType,
		"healthy":     healthy,
	}).Debug("Recorded worker health")
}

// GetMessageProcessedCount returns the count of processed messages
func (w *WorkerMetricsService) GetMessageProcessedCount(workerType models.WorkerType) (successful, failed int64) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if counts, exists := w.messageProcessedCount[workerType]; exists {
		return counts["success"], counts["failure"]
	}
	return 0, 0
}

// GetTotalMessageCount returns total message statistics
func (w *WorkerMetricsService) GetTotalMessageCount() (total, successful, failed int64) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.totalMessages, w.totalSuccessful, w.totalFailed
}

// GetAverageProcessingTime returns the average processing time for a worker type
func (w *WorkerMetricsService) GetAverageProcessingTime(workerType models.WorkerType) time.Duration {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.averageProcessingTime[workerType]
}

// GetProcessingTimeStats returns processing time statistics
func (w *WorkerMetricsService) GetProcessingTimeStats(workerType models.WorkerType) (avg, min, max time.Duration, count int) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	times, exists := w.messageProcessingTime[workerType]
	if !exists || len(times) == 0 {
		return 0, 0, 0, 0
	}

	var total time.Duration
	min = times[0]
	max = times[0]

	for _, duration := range times {
		total += duration
		if duration < min {
			min = duration
		}
		if duration > max {
			max = duration
		}
	}

	avg = total / time.Duration(len(times))
	return avg, min, max, len(times)
}

// GetQueueDepth returns the current queue depth
func (w *WorkerMetricsService) GetQueueDepth(queueName string) int {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.queueDepth[queueName]
}

// GetAllQueueDepths returns all queue depths
func (w *WorkerMetricsService) GetAllQueueDepths() map[string]int {
	w.mu.RLock()
	defer w.mu.RUnlock()

	result := make(map[string]int)
	for queueName, depth := range w.queueDepth {
		result[queueName] = depth
	}
	return result
}

// GetWorkerHealth returns the health status of a worker
func (w *WorkerMetricsService) GetWorkerHealth(workerType models.WorkerType) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	return w.workerHealth[workerType]
}

// GetAllWorkerHealth returns health status of all workers
func (w *WorkerMetricsService) GetAllWorkerHealth() map[models.WorkerType]bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	result := make(map[models.WorkerType]bool)
	for workerType, healthy := range w.workerHealth {
		result[workerType] = healthy
	}
	return result
}

// GetSuccessRate returns the success rate for a worker type
func (w *WorkerMetricsService) GetSuccessRate(workerType models.WorkerType) float64 {
	successful, failed := w.GetMessageProcessedCount(workerType)
	total := successful + failed

	if total == 0 {
		return 0.0
	}

	return float64(successful) / float64(total)
}

// GetOverallSuccessRate returns the overall success rate across all workers
func (w *WorkerMetricsService) GetOverallSuccessRate() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if w.totalMessages == 0 {
		return 0.0
	}

	return float64(w.totalSuccessful) / float64(w.totalMessages)
}

// GetUptime returns how long the metrics service has been running
func (w *WorkerMetricsService) GetUptime() time.Duration {
	return time.Since(w.startTime)
}

// GetMetricsSummary returns a comprehensive summary of all metrics
func (w *WorkerMetricsService) GetMetricsSummary() map[string]interface{} {
	w.mu.RLock()
	defer w.mu.RUnlock()

	summary := map[string]interface{}{
		"uptime":               w.GetUptime().String(),
		"total_messages":       w.totalMessages,
		"successful_messages":  w.totalSuccessful,
		"failed_messages":      w.totalFailed,
		"overall_success_rate": w.GetOverallSuccessRate(),
		"worker_stats":         make(map[string]interface{}),
		"queue_depths":         w.queueDepth,
		"worker_health":        w.workerHealth,
	}

	// Add per-worker statistics
	workerStats := make(map[string]interface{})
	for workerType := range w.messageProcessedCount {
		successful, failed := w.GetMessageProcessedCount(workerType)
		avg, min, max, count := w.GetProcessingTimeStats(workerType)

		workerStats[string(workerType)] = map[string]interface{}{
			"successful_messages": successful,
			"failed_messages":     failed,
			"success_rate":        w.GetSuccessRate(workerType),
			"avg_processing_time": avg.String(),
			"min_processing_time": min.String(),
			"max_processing_time": max.String(),
			"processing_samples":  count,
			"healthy":             w.workerHealth[workerType],
		}
	}
	summary["worker_stats"] = workerStats

	return summary
}

// ResetMetrics resets all metrics (useful for testing)
func (w *WorkerMetricsService) ResetMetrics() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.messageProcessedCount = make(map[models.WorkerType]map[string]int64)
	w.messageProcessingTime = make(map[models.WorkerType][]time.Duration)
	w.queueDepth = make(map[string]int)
	w.workerHealth = make(map[models.WorkerType]bool)
	w.averageProcessingTime = make(map[models.WorkerType]time.Duration)
	w.totalMessages = 0
	w.totalSuccessful = 0
	w.totalFailed = 0
	w.startTime = time.Now()

	w.logger.Info("Worker metrics reset")
}

// updateAverageProcessingTime updates the average processing time for a worker type
func (w *WorkerMetricsService) updateAverageProcessingTime(workerType models.WorkerType) {
	times := w.messageProcessingTime[workerType]
	if len(times) == 0 {
		w.averageProcessingTime[workerType] = 0
		return
	}

	var total time.Duration
	for _, duration := range times {
		total += duration
	}

	w.averageProcessingTime[workerType] = total / time.Duration(len(times))
}

// LogMetricsSummary logs a summary of current metrics
func (w *WorkerMetricsService) LogMetricsSummary() {
	summary := w.GetMetricsSummary()

	w.logger.WithFields(logrus.Fields{
		"uptime":               summary["uptime"],
		"total_messages":       summary["total_messages"],
		"successful_messages":  summary["successful_messages"],
		"failed_messages":      summary["failed_messages"],
		"overall_success_rate": summary["overall_success_rate"],
		"queue_depths":         summary["queue_depths"],
		"worker_health":        summary["worker_health"],
	}).Info("Worker metrics summary")
}

// StartPeriodicLogging starts periodic logging of metrics summary
func (w *WorkerMetricsService) StartPeriodicLogging(interval time.Duration) {
	ticker := time.NewTicker(interval)

	go func() {
		for range ticker.C {
			w.LogMetricsSummary()
		}
	}()
}
