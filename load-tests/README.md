# Load Testing with k6

This directory contains k6 load test scripts for the EAI Agent Gateway application.

## Prerequisites

- k6 is available in your development environment (added to flake.nix)
- The application is running locally or accessible via the configured BASE_URL

## Usage

### Basic Load Test

Run the main load test script:

```bash
just load-test
```

### Custom Base URL

You can specify a custom base URL for testing against different environments:

```bash
BASE_URL=http://staging.example.com k6 run load-tests/main.js
```

### Direct k6 Commands

You can also run k6 directly with additional options:

```bash
# Run with verbose output
k6 run --verbose load-tests/main.js

# Run with custom stages
k6 run --stage 30s:10 --stage 1m:10 --stage 30s:0 load-tests/main.js

# Run with custom thresholds
k6 run --threshold http_req_duration="p(95)<300" load-tests/main.js
```

## Test Configuration

The main test script (`main.js`) includes:

- **Stages**: Ramp up, steady load, and ramp down phases
- **Thresholds**: Performance requirements for response time and error rate
- **Base URL**: Configurable via environment variable
- **Health Check**: Basic endpoint testing

## Adding New Test Scenarios

1. Edit `load-tests/main.js`
2. Add new HTTP requests in the default function
3. Add appropriate checks for response validation
4. Consider adding new test files for specific scenarios

## Example Test Scenarios

- Health check endpoints
- Agent creation and management
- Message processing
- Webhook handling
- Authentication flows

## Monitoring

k6 provides detailed metrics including:
- Request rate (RPS)
- Response time percentiles
- Error rates
- Resource usage

## Best Practices

1. Start with low load and gradually increase
2. Test against staging environments first
3. Monitor application logs during tests
4. Set realistic thresholds based on requirements
5. Use different test scenarios for different endpoints 