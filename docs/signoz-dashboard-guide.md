# SigNoz Dashboard Guide for End-to-End Message Processing

This guide provides comprehensive instructions for creating SigNoz dashboards to monitor the end-to-end message processing pipeline, leveraging the distributed tracing implementation in the EAI Agent Gateway.

## Trace Structure Overview

The end-to-end trace spans multiple services and includes these key spans:

1. **`user_message_e2e`** - Main span from webhook receipt to RabbitMQ publish
2. **`Worker user_message_worker process_user_message`** - Worker processing span
3. **`audio_transcription`** - Audio processing (if applicable)
4. **`thread_management`** - Thread creation/retrieval
5. **`google_agent_engine_call`** - AI API call
6. **`response_processing`** - Response parsing and formatting
7. **`deliver_response`** - Final response delivery

## Key Attributes for Filtering

### Message Classification
- `message.type`: `"audio"` | `"text"`
- `message.is_audio`: `true` | `false`
- `message.length`: integer

### Processing Status
- `task.success`: `true` | `false`
- `task.will_retry`: `true` | `false`
- `task.error_type`: `"none"` | `"retriable"` | `"permanent"`

### Transcription Status
- `transcription.status`: `"started"` | `"attempting"` | `"completed"` | `"failed"`
- `transcription.result`: `"success"` | `"error"` | `"service_unavailable"` | `"empty_content"`
- `transcription.error_type`: `"network_error"` | `"auth_error"` | `"api_validation_error"` | `"rate_limit_error"` | `"format_error"` | `"download_error"` | `"service_error"` | `"unknown_error"`
- `transcription.fallback_used`: `true` | `false`
- `audio.format`: `"oga"` | `"mp3"` | `"m4a"` | etc.

### Response Status
- `response.status`: `"completed"` | `"failed"` | `"processing"`
- `response.has_data`: `true` | `false`
- `response.has_error`: `true` | `false`

### Service Information
- `service.name`: `"eai-gateway"` | `"eai-worker"`
- `provider`: `"google_agent_engine"`
- `user.number`: string

## Dashboard Panels to Create

### 1. End-to-End Duration Overview

**Panel Type:** Time Series

**Query:**
```sql
SELECT 
  percentile(duration, 50) as p50,
  percentile(duration, 95) as p95,
  percentile(duration, 99) as p99
FROM traces 
WHERE operation_name = 'user_message_e2e'
GROUP BY timestamp
ORDER BY timestamp
```

**Purpose:** Track overall system performance and latency trends.

### 2. Audio vs Text Processing Comparison

**Panel Type:** Time Series (Multi-series)

**Query:**
```sql
SELECT 
  message.type,
  avg(duration) as avg_duration
FROM traces 
WHERE operation_name = 'user_message_e2e'
GROUP BY message.type, timestamp
ORDER BY timestamp
```

**Purpose:** Compare processing times between audio and text messages.

### 3. Success Rate by Message Type

**Panel Type:** Stat/Gauge

**Query:**
```sql
SELECT 
  message.type,
  count(*) FILTER (WHERE task.success = true) * 100.0 / count(*) as success_rate
FROM traces 
WHERE operation_name = 'user_message_e2e'
GROUP BY message.type
```

**Purpose:** Monitor completion rates for different message types.

### 4. Retry Analysis

**Panel Type:** Bar Chart

**Query:**
```sql
SELECT 
  task.error_type,
  count(*) as count
FROM traces 
WHERE operation_name LIKE '%process_user_message%'
  AND task.error_type != 'none'
GROUP BY task.error_type
```

**Purpose:** Analyze retry patterns and error classifications.

### 5. Processing Time Breakdown

**Panel Type:** Stacked Bar Chart

**Query:**
```sql
SELECT 
  operation_name,
  avg(duration) as avg_duration
FROM traces 
WHERE trace_id IN (
  SELECT trace_id FROM traces 
  WHERE operation_name = 'user_message_e2e'
)
GROUP BY operation_name
```

**Purpose:** Identify bottlenecks in specific processing steps.

### 6. Transcription Success Rate

**Panel Type:** Stat/Gauge

**Query:**
```sql
SELECT 
  audio.format,
  count(*) FILTER (WHERE transcription.result = 'success') * 100.0 / count(*) as success_rate
FROM traces 
WHERE operation_name = 'audio_transcription'
GROUP BY audio.format
```

**Purpose:** Monitor transcription success rates by audio format.

### 7. Transcription Error Analysis

**Panel Type:** Pie Chart

**Query:**
```sql
SELECT 
  transcription.error_type,
  count(*) as count
FROM traces 
WHERE operation_name = 'audio_transcription'
  AND transcription.status = 'failed'
GROUP BY transcription.error_type
```

**Purpose:** Analyze distribution of transcription failure types.

## Dashboard Template Variables

### Message Type Filter
```yaml
Variable Name: $message_type
Query: SELECT DISTINCT message.type FROM traces WHERE operation_name = 'user_message_e2e'
Values: ["all", "audio", "text"]
```

### Time Range Filter
```yaml
Variable Name: $time_range
Default: "Last 1 hour"
Options: ["Last 5m", "Last 1h", "Last 24h", "Last 7d"]
```

### Provider Filter
```yaml
Variable Name: $provider
Query: SELECT DISTINCT provider FROM traces WHERE operation_name = 'user_message_e2e'
Values: ["all", "google_agent_engine"]
```

## Useful Panel Filters

### Filter by Message Type
```sql
WHERE message.type = '$message_type' OR '$message_type' = 'all'
```

### Filter by Success/Failure
```sql
WHERE task.success = true  -- Only successful requests
```

### Filter by Audio Messages Only
```sql
WHERE message.is_audio = true
```

### Filter by Failed Requests That Will Retry
```sql
WHERE task.will_retry = true
```

### Filter by Timeout Errors Specifically
```sql
WHERE task.error LIKE '%timeout%' OR task.error LIKE '%deadline exceeded%'
```

## Advanced Queries

### End-to-End Duration Distribution
```sql
SELECT 
  histogram_quantile(0.5, duration) as median,
  histogram_quantile(0.95, duration) as p95,
  histogram_quantile(0.99, duration) as p99
FROM traces 
WHERE operation_name = 'user_message_e2e'
  AND timestamp >= now() - interval '1 hour'
```

### Slowest Processing Steps
```sql
SELECT 
  operation_name,
  max(duration) as max_duration,
  avg(duration) as avg_duration,
  count(*) as count
FROM traces 
WHERE trace_id IN (
  SELECT trace_id FROM traces 
  WHERE operation_name = 'user_message_e2e'
    AND timestamp >= now() - interval '1 hour'
)
GROUP BY operation_name
ORDER BY avg_duration DESC
```

### Error Rate Trends
```sql
SELECT 
  time_bucket('5 minutes', timestamp) as time_bucket,
  count(*) FILTER (WHERE task.success = false) * 100.0 / count(*) as error_rate
FROM traces 
WHERE operation_name = 'user_message_e2e'
GROUP BY time_bucket
ORDER BY time_bucket
```

### Timeout Analysis
```sql
SELECT 
  time_bucket('10 minutes', timestamp) as time_bucket,
  count(*) FILTER (WHERE task.error LIKE '%timeout%' OR task.error LIKE '%deadline exceeded%') as timeout_count,
  count(*) as total_count
FROM traces 
WHERE operation_name = 'user_message_e2e'
GROUP BY time_bucket
ORDER BY time_bucket
```

## Alert Conditions

### High End-to-End Latency Alert
**Condition:**
```sql
avg(duration) FROM traces 
WHERE operation_name = 'user_message_e2e' 
  AND timestamp >= now() - interval '5 minutes'
> 30000000000  -- 30 seconds in nanoseconds
```

**Purpose:** Alert when average processing time exceeds 30 seconds.

### High Error Rate Alert
**Condition:**
```sql
count(*) FILTER (WHERE task.success = false) * 100.0 / count(*)
FROM traces 
WHERE operation_name = 'user_message_e2e'
  AND timestamp >= now() - interval '5 minutes'
> 10  -- More than 10% error rate
```

**Purpose:** Alert when error rate exceeds 10% over 5 minutes.

### Retry Storm Alert
**Condition:**
```sql
count(*) FILTER (WHERE task.will_retry = true)
FROM traces 
WHERE operation_name LIKE '%process_user_message%'
  AND timestamp >= now() - interval '5 minutes'
> 50  -- More than 50 retries in 5 minutes
```

**Purpose:** Alert when there are too many retriable errors.

## Dashboard Organization Recommendations

### Main Overview Dashboard
- End-to-End Duration Overview (top panel)
- Success Rate by Message Type (top right)
- Audio vs Text Processing Comparison (middle)
- Processing Time Breakdown (bottom)

### Error Analysis Dashboard
- Retry Analysis (main panel)
- Error Rate Trends (time series)
- Timeout Analysis (detailed breakdown)
- Failed Request Details (table view)

### Performance Deep Dive Dashboard
- Slowest Processing Steps (main focus)
- Duration Distribution (percentiles)
- Service-specific breakdowns
- Resource utilization correlation

## Implementation Notes

### Time Units
- SigNoz stores duration in nanoseconds
- Convert to milliseconds for display: `duration / 1000000`
- Convert to seconds for display: `duration / 1000000000`

### Trace Correlation
- Use `trace_id` to correlate spans across the entire pipeline
- Filter parent spans to get complete e2e journeys
- Use `parent_span_id` for hierarchical analysis

### Performance Tips
- Use time range filters to limit query scope
- Create indexed views for frequently accessed metrics
- Cache dashboard queries for better performance
- Use sampling for high-volume analysis

## Getting Started

1. **Create Template Variables** first to enable dynamic filtering
2. **Start with Overview Dashboard** to get general system health
3. **Add Error Analysis** panels for operational monitoring
4. **Implement Alerts** for proactive issue detection
5. **Expand with Deep Dive** dashboards as needed

This implementation provides comprehensive visibility into your message processing pipeline, enabling you to:
- Track complete user journey performance
- Identify bottlenecks and optimization opportunities
- Monitor retry behavior and error patterns
- Compare performance between different message types
- Set up proactive alerting for operational issues