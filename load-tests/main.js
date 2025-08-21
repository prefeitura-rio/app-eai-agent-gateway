import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

// Custom metrics
const messageCompletionTime = new Trend('message_completion_time');
const successfulMessageCompletionTime = new Trend('successful_message_completion_time');
const failedMessageCompletionTime = new Trend('failed_message_completion_time');
const messageSuccessRate = new Rate('message_success_rate');

// Distribution tracking
let successfulTimes = [];
let failedTimes = [];
let allTimes = [];

// Log level configuration
const LOG_LEVEL = __ENV.LOG_LEVEL || 'INFO'; // DEBUG, INFO, WARN, ERROR, NONE
const LOG_LEVELS = {
  DEBUG: 0,
  INFO: 1,
  WARN: 2,
  ERROR: 3,
  NONE: 4
};

function log(level, message) {
  if (LOG_LEVELS[level] >= LOG_LEVELS[LOG_LEVEL]) {
    console.log(`[${level}] ${message}`);
  }
}

function calculateDistributionStats(times, label) {
  if (times.length === 0) {
    log('INFO', `${label}: No data available`);
    return;
  }
  
  const sorted = times.sort((a, b) => a - b);
  const count = sorted.length;
  const min = sorted[0];
  const max = sorted[count - 1];
  const sum = sorted.reduce((acc, val) => acc + val, 0);
  const mean = sum / count;
  const median = count % 2 === 0 
    ? (sorted[count/2 - 1] + sorted[count/2]) / 2 
    : sorted[Math.floor(count/2)];
  
  // Percentiles
  const p50 = sorted[Math.floor(count * 0.5)];
  const p75 = sorted[Math.floor(count * 0.75)];
  const p90 = sorted[Math.floor(count * 0.9)];
  const p95 = sorted[Math.floor(count * 0.95)];
  const p99 = sorted[Math.floor(count * 0.99)];
  
  log('INFO', `=== ${label} Distribution ===`);
  log('INFO', `Count: ${count}`);
  log('INFO', `Min: ${min}ms, Max: ${max}ms`);
  log('INFO', `Mean: ${mean.toFixed(2)}ms, Median: ${median}ms`);
  log('INFO', `P50: ${p50}ms, P75: ${p75}ms, P90: ${p90}ms, P95: ${p95}ms, P99: ${p99}ms`);
  
  // Histogram buckets (0-5s, 5-10s, 10-30s, 30-60s, 60s+)
  const buckets = [0, 5000, 10000, 30000, 60000, Infinity];
  const bucketLabels = ['0-5s', '5-10s', '10-30s', '30-60s', '60s+'];
  const histogram = new Array(buckets.length - 1).fill(0);
  
  for (const time of times) {
    for (let i = 0; i < buckets.length - 1; i++) {
      if (time >= buckets[i] && time < buckets[i + 1]) {
        histogram[i]++;
        break;
      }
    }
  }
  
  log('INFO', `Histogram:`);
  for (let i = 0; i < histogram.length; i++) {
    const percentage = ((histogram[i] / count) * 100).toFixed(1);
    log('INFO', `  ${bucketLabels[i]}: ${histogram[i]} (${percentage}%)`);
  }
  log('INFO', `=======================`);
}

// Test configuration
export const options = {
  stages: [
    { duration: '1m', target: 1500 },
    { duration: '3m', target: 1500 },
    { duration: '1m', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(95)<5000'], // 95% of requests must complete below 5s
    http_req_failed: ['rate<0.1'],     // Error rate must be less than 10%
    message_completion_time: ['p(99)<100000'], // 99% of all messages must complete within 100 seconds
    successful_message_completion_time: ['p(99)<100000'], // 99% of successful messages must complete within 100 seconds
    message_success_rate: ['rate>0.99'], // 99% of messages must complete successfully
  },

};

const BASE_URL = __ENV.BASE_URL || 'https://eai-agent-gateway-superapp-staging.squirrel-regulus.ts.net';
const BEARER_TOKEN = __ENV.BEARER_TOKEN;
const MESSAGES_PER_USER = parseInt(__ENV.MESSAGES_PER_USER, 10) || 3;
const MESSAGE_DELAY_SECONDS = parseInt(__ENV.MESSAGE_DELAY_SECONDS, 10) || 10; // Delay between messages to emulate user reading/thinking/typing time

function generateRandomUserNumber() {
  const randomPart = Math.floor(Math.random() * 10000000000).toString().padStart(10, '0');
  return `55${randomPart}`;
}

function generateRandomString(length = 32) {
  if (Math.random() < 0.25) {
    return 'Qual o presidente da australia? Use a tool google_search pra responder';
  }
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  let result = '';
  for (let i = 0; i < length; i++) {
    result += chars.charAt(Math.floor(Math.random() * chars.length));
  }
  return result;
}

const LOAD_TEST_MESSAGE =
  'I am a virtual user created by k6 for a load test. Please treat all my following messages as if they were meaningful, and respond as if you were talking to a real user. Make up meanings and answers as needed.';

function sendMessageAndPoll(userNumber, message, headers) {
  const submitTime = new Date();
  const bodyData = {
    user_number: userNumber,
    message: message,
    metadata: {}
  };
  const response = http.post(
    `${BASE_URL}/api/v1/message/webhook/user`,
    JSON.stringify(bodyData),
    { headers: headers }
  );
  check(response, {
    'initial request status is 200 or 201': (r) => r.status === 200 || r.status === 201,
    'initial response has message_id': (r) => {
      try {
        const jsonResponse = JSON.parse(r.body);
        return jsonResponse.message_id !== undefined;
      } catch (e) {
        return false;
      }
    },
    'initial response has polling_endpoint': (r) => {
      try {
        const jsonResponse = JSON.parse(r.body);
        return jsonResponse.polling_endpoint !== undefined;
      } catch (e) {
        return false;
      }
    }
  });
  if (response.status !== 200 && response.status !== 201) {
    return null;
  }
  let jsonResponse;
  try {
    jsonResponse = JSON.parse(response.body);
  } catch (e) {
    return null;
  }
  const messageId = jsonResponse.message_id;
  const pollingEndpoint = jsonResponse.polling_endpoint;
  if (!pollingEndpoint || !messageId) {
    return null;
  }
  
  const pollUrl = `${BASE_URL}${pollingEndpoint}`;
  let pollCount = 0;
  let finalStatus = null;
  while (true) {
    pollCount++;
    log('DEBUG', `Polling message ${messageId} (attempt ${pollCount})`);
    const pollResponse = http.get(pollUrl, { headers: headers });
    check(pollResponse, {
      'poll request successful': (r) => r.status === 202 || r.status === 200,
    });
    if (pollResponse.status !== 202 && pollResponse.status !== 200) {
      const currentTime = new Date();
      const timeDiff = currentTime - submitTime;
      let responseMessage = 'No response body';
      try {
        const responseData = JSON.parse(pollResponse.body);
        responseMessage = responseData.message || responseData.detail || JSON.stringify(responseData);
      } catch (e) {
        responseMessage = pollResponse.body || 'No response body';
      }
      log('ERROR', `Message ID: ${messageId} | Submit: ${submitTime.toISOString()} | Error: ${currentTime.toISOString()} | Duration: ${timeDiff}ms | Poll Count: ${pollCount} | Status: ${pollResponse.status} | Message: ${responseMessage}`);
      
      // Record failed message completion time and mark as failed
      messageCompletionTime.add(timeDiff);
      failedMessageCompletionTime.add(timeDiff);
      messageSuccessRate.add(false);
      
      // Track for distribution analysis
      failedTimes.push(timeDiff);
      allTimes.push(timeDiff);
      break;
    }
    let pollData;
    try {
      pollData = JSON.parse(pollResponse.body);
    } catch (e) {
      break;
    }
    const currentStatus = pollData.status;
    if (currentStatus === 'completed' || currentStatus === 'failed' || currentStatus === 'error') {
      finalStatus = currentStatus;
      
      // Record completion time and success status
      const completionTime = new Date();
      const totalTime = completionTime - submitTime;
      messageCompletionTime.add(totalTime);
      
      if (currentStatus === 'completed') {
        successfulMessageCompletionTime.add(totalTime);
        messageSuccessRate.add(true);
        
        // Track for distribution analysis
        successfulTimes.push(totalTime);
        allTimes.push(totalTime);
      } else {
        failedMessageCompletionTime.add(totalTime);
        messageSuccessRate.add(false);
        
        // Track for distribution analysis
        failedTimes.push(totalTime);
        allTimes.push(totalTime);
      }
      
      log('INFO', `Message ID: ${messageId} | Submit: ${submitTime.toISOString()} | Complete: ${completionTime.toISOString()} | Duration: ${totalTime}ms | Poll Count: ${pollCount} | Status: ${currentStatus}`);
      break;
    }
    sleep(5);
  }
  check({ finalStatus: finalStatus }, {
    'message processing completed': (data) => data.finalStatus === 'completed',
  });
  
  // Log summary for completed messages
  if (finalStatus === 'completed') {
    log('DEBUG', `Message ${messageId} completed successfully`);
  } else if (finalStatus === 'failed' || finalStatus === 'error') {
    log('WARN', `Message ${messageId} failed with status: ${finalStatus}`);
  }
  
  return finalStatus;
}

export default function () {
  const userNumber = generateRandomUserNumber();
  const headers = {
    'Content-Type': 'application/json'
  };
  
  // Only add Authorization header if BEARER_TOKEN is set
  if (BEARER_TOKEN) {
    headers['Authorization'] = `Bearer ${BEARER_TOKEN}`;
  }
  
  // First message: load test warning
  sendMessageAndPoll(userNumber, LOAD_TEST_MESSAGE, headers);
  
  // Add delay between messages to emulate user reading/thinking/typing time
  if (MESSAGES_PER_USER > 1) {
    sleep(MESSAGE_DELAY_SECONDS);
  }
  
  // Subsequent messages: random strings
  for (let i = 1; i < MESSAGES_PER_USER; i++) {
    const randomMsg = generateRandomString(32);
    sendMessageAndPoll(userNumber, randomMsg, headers);
    
    // Add delay between messages (except after the last one)
    if (i < MESSAGES_PER_USER - 1) {
      sleep(MESSAGE_DELAY_SECONDS);
    }
  }
}

// Teardown function to log distribution statistics
export function teardown() {
  log('INFO', '');
  log('INFO', '=== LOAD TEST COMPLETED ===');
  log('INFO', '');
  
  calculateDistributionStats(successfulTimes, 'Successful Messages');
  calculateDistributionStats(failedTimes, 'Failed Messages');
  calculateDistributionStats(allTimes, 'All Messages');
  
  log('INFO', '');
  log('INFO', '=== SUMMARY ===');
  log('INFO', `Total Messages: ${allTimes.length}`);
  log('INFO', `Successful: ${successfulTimes.length} (${((successfulTimes.length / allTimes.length) * 100).toFixed(1)}%)`);
  log('INFO', `Failed: ${failedTimes.length} (${((failedTimes.length / allTimes.length) * 100).toFixed(1)}%)`);
  log('INFO', '================');
} 