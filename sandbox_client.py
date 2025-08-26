#!/usr/bin/env python3
"""
EAí Agent Gateway - Interactive Sandbox Client

This script provides an interactive interface to test the EAí Agent Gateway API.
You can send messages and poll for responses in real-time.
"""

import json
import time
import requests
import sys
from typing import Optional, Dict, Any
from datetime import datetime


class EAIGatewayClient:
    """Client for interacting with the EAí Agent Gateway API."""
    
    def __init__(self, base_url: str = "http://localhost:8000"):
        """
        Initialize the client.
        
        Args:
            base_url: Base URL of the EAí Agent Gateway API
        """
        self.base_url = base_url.rstrip('/')
        self.session = requests.Session()
        
    def send_user_message(self, user_id: str, content: str, audio_url: Optional[str] = None) -> Optional[str]:
        """
        Send a user message to the gateway.
        
        Args:
            user_id: Unique identifier for the user
            content: Message content
            audio_url: Optional URL to audio file
            
        Returns:
            Message ID if successful, None if failed
        """
        url = f"{self.base_url}/api/v1/message/webhook/user"
        
        payload = {
            "user_id": user_id,
            "content": content
        }
        
        if audio_url:
            payload["audio_url"] = audio_url
            
        try:
            print(f"🚀 Sending message to {url}")
            response = self.session.post(url, json=payload, timeout=10)
            response.raise_for_status()
            
            data = response.json()
            message_id = data.get("message_id")
            status = data.get("status")
            
            print(f"✅ Message sent successfully!")
            print(f"   Message ID: {message_id}")
            print(f"   Status: {status}")
            
            return message_id
            
        except requests.exceptions.RequestException as e:
            print(f"❌ Failed to send message: {e}")
            if hasattr(e, 'response') and e.response is not None:
                try:
                    error_data = e.response.json()
                    print(f"   Error details: {error_data}")
                except:
                    print(f"   Response: {e.response.text}")
            return None
    
    def send_agent_message(self, agent_id: str, content: str) -> Optional[str]:
        """
        Send an agent message to the gateway.
        
        Args:
            agent_id: Unique identifier for the agent
            content: Message content
            
        Returns:
            Message ID if successful, None if failed
        """
        url = f"{self.base_url}/api/v1/message/webhook/agent"
        
        payload = {
            "agent_id": agent_id,
            "content": content
        }
            
        try:
            print(f"🤖 Sending agent message to {url}")
            response = self.session.post(url, json=payload, timeout=10)
            response.raise_for_status()
            
            data = response.json()
            message_id = data.get("message_id")
            status = data.get("status")
            
            print(f"✅ Agent message sent successfully!")
            print(f"   Message ID: {message_id}")
            print(f"   Status: {status}")
            
            return message_id
            
        except requests.exceptions.RequestException as e:
            print(f"❌ Failed to send agent message: {e}")
            if hasattr(e, 'response') and e.response is not None:
                try:
                    error_data = e.response.json()
                    print(f"   Error details: {error_data}")
                except:
                    print(f"   Response: {e.response.text}")
            return None
    
    def poll_response(self, message_id: str, max_attempts: int = 30, poll_interval: float = 2.0) -> Optional[Dict[str, Any]]:
        """
        Poll for a message response.
        
        Args:
            message_id: ID of the message to poll for
            max_attempts: Maximum number of polling attempts
            poll_interval: Time between polls in seconds
            
        Returns:
            Response data if available, None if failed or timed out
        """
        url = f"{self.base_url}/api/v1/message/response"
        
        print(f"🔄 Polling for response (message ID: {message_id})")
        print(f"   Max attempts: {max_attempts}, Poll interval: {poll_interval}s")
        
        for attempt in range(1, max_attempts + 1):
            try:
                response = self.session.get(url, params={"message_id": message_id}, timeout=10)
                response.raise_for_status()
                
                data = response.json()
                status = data.get("status")
                
                print(f"   Attempt {attempt}/{max_attempts}: Status = {status}")
                
                if status == "completed":
                    print(f"✅ Response received!")
                    return data
                elif status == "failed":
                    print(f"❌ Message processing failed")
                    return data
                elif status in ["pending", "processing"]:
                    if attempt < max_attempts:
                        time.sleep(poll_interval)
                        continue
                    else:
                        print(f"⏰ Timeout waiting for response")
                        return None
                else:
                    print(f"⚠️  Unknown status: {status}")
                    return data
                    
            except requests.exceptions.RequestException as e:
                print(f"❌ Failed to poll response (attempt {attempt}): {e}")
                if attempt < max_attempts:
                    time.sleep(poll_interval)
                    continue
                return None
        
        return None
    
    def get_debug_info(self, message_id: str) -> Optional[Dict[str, Any]]:
        """
        Get debug information for a message.
        
        Args:
            message_id: ID of the message to get debug info for
            
        Returns:
            Debug info if available, None if failed
        """
        url = f"{self.base_url}/api/v1/message/debug/task-status"
        
        try:
            print(f"🔍 Getting debug info for message {message_id}")
            response = self.session.get(url, params={"message_id": message_id}, timeout=10)
            response.raise_for_status()
            
            data = response.json()
            print(f"✅ Debug info retrieved!")
            return data
            
        except requests.exceptions.RequestException as e:
            print(f"❌ Failed to get debug info: {e}")
            return None
    
    def check_health(self) -> bool:
        """
        Check if the gateway is healthy.
        
        Returns:
            True if healthy, False otherwise
        """
        url = f"{self.base_url}/health"
        
        try:
            response = self.session.get(url, timeout=5)
            response.raise_for_status()
            
            data = response.json()
            status = data.get("status")
            
            if status == "healthy":
                print(f"✅ Gateway is healthy")
                return True
            else:
                print(f"⚠️  Gateway status: {status}")
                return False
                
        except requests.exceptions.RequestException as e:
            print(f"❌ Gateway health check failed: {e}")
            return False


def print_header():
    """Print the application header."""
    print("=" * 60)
    print("🤖 EAí Agent Gateway - Interactive Sandbox Client")
    print("=" * 60)
    print()


def print_menu():
    """Print the main menu."""
    print("\n📋 Available Actions:")
    print("1. Send user message")
    print("2. Send agent message")
    print("3. Poll for response")
    print("4. Get debug info")
    print("5. Check gateway health")
    print("6. Send message and wait for response")
    print("q. Quit")
    print()


def format_response(data: Dict[str, Any]) -> str:
    """Format response data for display."""
    lines = []
    lines.append(f"Message ID: {data.get('message_id', 'N/A')}")
    lines.append(f"Status: {data.get('status', 'N/A')}")
    lines.append(f"Timestamp: {data.get('timestamp', 'N/A')}")
    
    if data.get('content'):
        lines.append(f"Content: {data['content']}")
    
    if data.get('error'):
        lines.append(f"Error: {data['error']}")
    
    if data.get('metadata'):
        lines.append(f"Metadata: {json.dumps(data['metadata'], indent=2)}")
    
    return "\n".join(lines)


def format_debug_info(data: Dict[str, Any]) -> str:
    """Format debug info for display."""
    lines = []
    lines.append(f"Message ID: {data.get('message_id', 'N/A')}")
    lines.append(f"Status: {data.get('status', 'N/A')}")
    lines.append(f"Created At: {data.get('created_at', 'N/A')}")
    lines.append(f"Updated At: {data.get('updated_at', 'N/A')}")
    lines.append(f"Retry Count: {data.get('retry_count', 0)}")
    
    if data.get('last_error'):
        lines.append(f"Last Error: {data['last_error']}")
    
    if data.get('queue_info'):
        lines.append(f"Queue Info: {json.dumps(data['queue_info'], indent=2)}")
    
    if data.get('processing_log'):
        lines.append(f"Processing Log: {json.dumps(data['processing_log'], indent=2)}")
    
    return "\n".join(lines)


def main():
    """Main interactive loop."""
    print_header()
    
    # Initialize client
    client = EAIGatewayClient()
    
    # Check gateway health on startup
    print("🔍 Checking gateway health...")
    if not client.check_health():
        print("⚠️  Gateway appears to be unhealthy, but you can still try to use it.")
    
    print("\n💡 Tip: Use Ctrl+C to interrupt any operation")
    
    while True:
        try:
            print_menu()
            choice = input("Enter your choice: ").strip().lower()
            
            if choice == 'q' or choice == 'quit':
                print("👋 Goodbye!")
                break
            
            elif choice == '1':
                # Send user message
                print("\n📝 Send User Message")
                user_id = input("Enter user ID: ").strip()
                if not user_id:
                    print("❌ User ID cannot be empty")
                    continue
                
                content = input("Enter message content: ").strip()
                if not content:
                    print("❌ Message content cannot be empty")
                    continue
                
                audio_url = input("Enter audio URL (optional, press Enter to skip): ").strip()
                audio_url = audio_url if audio_url else None
                
                message_id = client.send_user_message(user_id, content, audio_url)
                if message_id:
                    print(f"\n💾 Saved message ID: {message_id}")
            
            elif choice == '2':
                # Send agent message
                print("\n🤖 Send Agent Message")
                agent_id = input("Enter agent ID: ").strip()
                if not agent_id:
                    print("❌ Agent ID cannot be empty")
                    continue
                
                content = input("Enter message content: ").strip()
                if not content:
                    print("❌ Message content cannot be empty")
                    continue
                
                message_id = client.send_agent_message(agent_id, content)
                if message_id:
                    print(f"\n💾 Saved message ID: {message_id}")
            
            elif choice == '3':
                # Poll for response
                print("\n🔄 Poll for Response")
                message_id = input("Enter message ID: ").strip()
                if not message_id:
                    print("❌ Message ID cannot be empty")
                    continue
                
                max_attempts = input("Max polling attempts (default 30): ").strip()
                max_attempts = int(max_attempts) if max_attempts.isdigit() else 30
                
                poll_interval = input("Poll interval in seconds (default 2.0): ").strip()
                try:
                    poll_interval = float(poll_interval) if poll_interval else 2.0
                except ValueError:
                    poll_interval = 2.0
                
                response_data = client.poll_response(message_id, max_attempts, poll_interval)
                if response_data:
                    print("\n📄 Response Data:")
                    print(format_response(response_data))
            
            elif choice == '4':
                # Get debug info
                print("\n🔍 Get Debug Info")
                message_id = input("Enter message ID: ").strip()
                if not message_id:
                    print("❌ Message ID cannot be empty")
                    continue
                
                debug_data = client.get_debug_info(message_id)
                if debug_data:
                    print("\n🐛 Debug Info:")
                    print(format_debug_info(debug_data))
            
            elif choice == '5':
                # Check gateway health
                print("\n❤️  Health Check")
                client.check_health()
            
            elif choice == '6':
                # Send message and wait for response
                print("\n🚀 Send Message and Wait for Response")
                
                message_type = input("Message type (user/agent): ").strip().lower()
                if message_type not in ['user', 'agent']:
                    print("❌ Message type must be 'user' or 'agent'")
                    continue
                
                if message_type == 'user':
                    user_id = input("Enter user ID: ").strip()
                    if not user_id:
                        print("❌ User ID cannot be empty")
                        continue
                    
                    content = input("Enter message content: ").strip()
                    if not content:
                        print("❌ Message content cannot be empty")
                        continue
                    
                    audio_url = input("Enter audio URL (optional, press Enter to skip): ").strip()
                    audio_url = audio_url if audio_url else None
                    
                    message_id = client.send_user_message(user_id, content, audio_url)
                else:
                    agent_id = input("Enter agent ID: ").strip()
                    if not agent_id:
                        print("❌ Agent ID cannot be empty")
                        continue
                    
                    content = input("Enter message content: ").strip()
                    if not content:
                        print("❌ Message content cannot be empty")
                        continue
                    
                    message_id = client.send_agent_message(agent_id, content)
                
                if message_id:
                    print(f"\n⏳ Waiting for response...")
                    response_data = client.poll_response(message_id)
                    if response_data:
                        print("\n📄 Final Response:")
                        print(format_response(response_data))
            
            else:
                print("❌ Invalid choice. Please try again.")
        
        except KeyboardInterrupt:
            print("\n\n⏹️  Operation interrupted by user")
            continue
        except EOFError:
            print("\n\n👋 Goodbye!")
            break
        except Exception as e:
            print(f"\n❌ Unexpected error: {e}")
            print("💡 Please try again or restart the script")


if __name__ == "__main__":
    main()