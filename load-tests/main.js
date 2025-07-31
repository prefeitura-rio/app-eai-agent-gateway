import http from 'k6/http';
import { check, sleep } from 'k6';

// Test configuration
export const options = {
  stages: [
    { duration: '30s', target: 100 },   // Ramp up to 5 users over 30 seconds
    { duration: '2m', target: 100 },    // Stay at 5 users for 1 minute
    { duration: '30s', target: 0 },   // Ramp down to 0 users over 30 seconds
  ],
  thresholds: {
    http_req_duration: ['p(95)<5000'], // 95% of requests must complete below 5s
    http_req_failed: ['rate<0.1'],     // Error rate must be less than 10%
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
    console.log(`Initial request failed: ${response.status} - ${response.body}`);
    return null;
  }
  let jsonResponse;
  try {
    jsonResponse = JSON.parse(response.body);
  } catch (e) {
    console.log('Failed to parse initial response as JSON');
    return null;
  }
  const messageId = jsonResponse.message_id;
  const pollingEndpoint = jsonResponse.polling_endpoint;
  if (!pollingEndpoint || !messageId) {
    console.log('Missing polling endpoint or message_id in response');
    return null;
  }
  const pollUrl = `${BASE_URL}${pollingEndpoint}`;
  let pollCount = 0;
  let finalStatus = null;
  while (true) {
    pollCount++;
    const pollResponse = http.get(pollUrl, { headers: headers });
    check(pollResponse, {
      'poll request successful': (r) => r.status === 202 || r.status === 200,
    });
    if (pollResponse.status !== 202 && pollResponse.status !== 200) {
      console.log(`Poll failed with status: ${pollResponse.status}`);
      break;
    }
    let pollData;
    try {
      pollData = JSON.parse(pollResponse.body);
    } catch (e) {
      console.log('Failed to parse poll response as JSON');
      break;
    }
    const currentStatus = pollData.status;
    if (currentStatus === 'completed' || currentStatus === 'failed' || currentStatus === 'error') {
      finalStatus = currentStatus;
      break;
    }
    sleep(15);
  }
  check({ finalStatus: finalStatus }, {
    'message processing completed': (data) => data.finalStatus === 'completed',
  });
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