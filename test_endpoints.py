#!/usr/bin/env python3
"""
Test script for EAI Agent Gateway endpoints.

Tests:
1. Submit a message to the user endpoint and print response
2. Submit messages to the history update endpoint
3. Submit a message with previous_message to the user endpoint

Usage:
    python test_endpoints.py <reasoning_engine_id>
"""

import requests
import time
import json
import sys
import random
import argparse

# Configuration
BASE_URL = "http://localhost:8000"
USER_NUMBER = f"5521{random.randint(100000000, 999999999)}"

# ANSI color codes
GREEN = "\033[92m"
BLUE = "\033[94m"
YELLOW = "\033[93m"
RED = "\033[91m"
RESET = "\033[0m"


def print_section(title):
    """Print a section header"""
    print(f"\n{BLUE}{'=' * 60}{RESET}")
    print(f"{BLUE}{title}{RESET}")
    print(f"{BLUE}{'=' * 60}{RESET}\n")


def print_success(message):
    """Print success message"""
    print(f"{GREEN}✓ {message}{RESET}")


def print_error(message):
    """Print error message"""
    print(f"{RED}✗ {message}{RESET}")


def print_info(message):
    """Print info message"""
    print(f"{YELLOW}→ {message}{RESET}")


def poll_for_response(message_id, max_attempts=30, interval=2):
    """Poll for message response"""
    url = f"{BASE_URL}/api/v1/message/response"
    params = {"message_id": message_id}

    for attempt in range(max_attempts):
        try:
            response = requests.get(url, params=params)
            response.raise_for_status()
            data = response.json()

            status = data.get("status")
            print_info(f"Attempt {attempt + 1}/{max_attempts}: Status = {status}")

            if status == "completed":
                return data
            elif status == "failed":
                error = data.get("error", "Unknown error")
                print_error(f"Task failed: {error}")
                return None

            time.sleep(interval)

        except requests.exceptions.RequestException as e:
            print_error(f"Polling error: {e}")
            time.sleep(interval)

    print_error("Polling timeout - response not ready")
    return None


def test_user_endpoint(reasoning_engine_id):
    """Test 1: Submit message to user endpoint"""
    print_section("TEST 1: User Endpoint")

    payload = {
        "user_number": USER_NUMBER,
        "message": "Hello! This is test message #1. What is 2+2?",
        "reasoning_engine_id": reasoning_engine_id
    }

    print_info("Sending message to user endpoint...")
    print(f"Payload: {json.dumps(payload, indent=2)}")

    try:
        response = requests.post(
            f"{BASE_URL}/api/v1/message/webhook/user",
            json=payload
        )
        response.raise_for_status()
        data = response.json()

        message_id = data.get("message_id")
        print_success(f"Message queued with ID: {message_id}")
        print_info(f"Polling endpoint: {data.get('polling_endpoint')}")

        # Poll for response
        print_info("Polling for response...")
        result = poll_for_response(message_id)

        if result:
            print_success("Response received!")
            print(f"\nResponse data:")
            print(json.dumps(result, indent=2))
            return True
        else:
            print_error("Failed to get response")
            return False

    except requests.exceptions.RequestException as e:
        print_error(f"Request failed: {e}")
        if hasattr(e.response, 'text'):
            print(f"Response: {e.response.text}")
        return False


def test_history_update_endpoint(reasoning_engine_id):
    """Test 2: Submit messages to history update endpoint"""
    print_section("TEST 2: History Update Endpoint")

    payload = {
        "user_number": USER_NUMBER,
        "messages": [
            {
                "content": "What is the capital of France?",
                "role": "human"
            },
            {
                "content": "The capital of France is Paris.",
                "role": "ai"
            },
            {
                "content": "What about Spain?",
                "role": "human"
            },
            {
                "content": "The capital of Spain is Madrid.",
                "role": "ai"
            }
        ],
        "reasoning_engine_id": reasoning_engine_id
    }

    print_info("Sending messages to history update endpoint...")
    print(f"Payload: {json.dumps(payload, indent=2)}")

    try:
        response = requests.post(
            f"{BASE_URL}/api/v1/message/webhook/update_history",
            json=payload
        )
        response.raise_for_status()
        data = response.json()

        print_success("History updated successfully!")
        print(f"\nResponse:")
        print(json.dumps(data, indent=2))
        return True

    except requests.exceptions.RequestException as e:
        print_error(f"Request failed: {e}")
        if hasattr(e.response, 'text'):
            print(f"Response: {e.response.text}")
        return False


def test_user_endpoint_with_previous_message(reasoning_engine_id):
    """Test 3: Submit message with previous_message to user endpoint"""
    print_section("TEST 3: User Endpoint with Previous Message")

    payload = {
        "user_number": USER_NUMBER,
        "message": "Can you summarize what we talked about?",
        "previous_message": "By the way, I really enjoyed our conversation about European capitals!",
        "reasoning_engine_id": reasoning_engine_id
    }

    print_info("Sending message with previous_message to user endpoint...")
    print(f"Payload: {json.dumps(payload, indent=2)}")

    try:
        response = requests.post(
            f"{BASE_URL}/api/v1/message/webhook/user",
            json=payload
        )
        response.raise_for_status()
        data = response.json()

        message_id = data.get("message_id")
        print_success(f"Message queued with ID: {message_id}")
        print_info(f"Polling endpoint: {data.get('polling_endpoint')}")

        # Poll for response
        print_info("Polling for response...")
        result = poll_for_response(message_id)

        if result:
            print_success("Response received!")
            print(f"\nResponse data:")
            print(json.dumps(result, indent=2))
            return True
        else:
            print_error("Failed to get response")
            return False

    except requests.exceptions.RequestException as e:
        print_error(f"Request failed: {e}")
        if hasattr(e.response, 'text'):
            print(f"Response: {e.response.text}")
        return False


def main():
    """Run all tests"""
    # Parse command line arguments
    parser = argparse.ArgumentParser(
        description="Test EAI Agent Gateway endpoints",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python test_endpoints.py 12345678
  python test_endpoints.py my-reasoning-engine-id
        """
    )
    parser.add_argument(
        "reasoning_engine_id",
        help="Reasoning Engine ID to use for all requests"
    )
    args = parser.parse_args()

    print(f"\n{GREEN}EAI Agent Gateway - Endpoint Test Suite{RESET}")
    print(f"Base URL: {BASE_URL}")
    print(f"User Number: {USER_NUMBER}")
    print(f"Reasoning Engine ID: {args.reasoning_engine_id}")

    results = []

    # Test 1
    result1 = test_user_endpoint(args.reasoning_engine_id)
    results.append(("User Endpoint", result1))

    # Wait a bit before next test
    time.sleep(2)

    # Test 2
    result2 = test_history_update_endpoint(args.reasoning_engine_id)
    results.append(("History Update Endpoint", result2))

    # Wait a bit before next test
    time.sleep(2)

    # Test 3
    result3 = test_user_endpoint_with_previous_message(args.reasoning_engine_id)
    results.append(("User Endpoint with Previous Message", result3))

    # Summary
    print_section("TEST SUMMARY")
    for test_name, result in results:
        if result:
            print_success(f"{test_name}")
        else:
            print_error(f"{test_name}")

    # Exit code
    all_passed = all(result for _, result in results)
    sys.exit(0 if all_passed else 1)


if __name__ == "__main__":
    main()
