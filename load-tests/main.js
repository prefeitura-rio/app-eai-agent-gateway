import http from 'k6/http';
import { check, sleep } from 'k6';
import { Rate, Trend } from 'k6/metrics';

// Custom metrics
const messageCompletionTime = new Trend('message_completion_time');
const messageSuccessRate = new Rate('message_success_rate');

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

// Test configuration
export const options = {
  stages: [
    { duration: '1m', target: 1000 },
    { duration: '3m', target: 1000 },
    { duration: '1m', target: 0 },
  ],
  thresholds: {
    http_req_duration: ['p(95)<5000'], // 95% of requests must complete below 5s
    http_req_failed: ['rate<0.1'],     // Error rate must be less than 10%
    message_completion_time: ['p(99)<100000'], // 99% of messages must complete within 100 seconds
    message_success_rate: ['rate>0.99'], // 99% of messages must complete successfully
  },
};

const BASE_URL = __ENV.BASE_URL || 'https://eai-agent-gateway-superapp-staging.squirrel-regulus.ts.net';
const BEARER_TOKEN = __ENV.BEARER_TOKEN;
const MESSAGES_PER_USER = parseInt(__ENV.MESSAGES_PER_USER, 10) || 3;

function generateRandomUserNumber() {
  const randomPart = Math.floor(Math.random() * 10000000000).toString().padStart(10, '0');
  return `55${randomPart}`;
}

function generateRandomString(length = 32) {
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
      messageSuccessRate.add(false);
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
      messageSuccessRate.add(currentStatus === 'completed');
      
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
  // Subsequent messages: random strings
  for (let i = 1; i < MESSAGES_PER_USER; i++) {
    const randomMsg = generateRandomString(32);
    sendMessageAndPoll(userNumber, randomMsg, headers);
  }
} 