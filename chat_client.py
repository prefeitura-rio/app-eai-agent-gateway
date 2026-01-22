#!/usr/bin/env python3
"""
EAí Agent Gateway - Chat Interface

A chat-like interface for talking to AI agents through the EAí Gateway.
Feels like a real chat conversation!
"""

import json
import time
import requests
import sys
import threading
import argparse
import os
from typing import Optional, Dict, Any
from datetime import datetime
import uuid


class ChatClient:
    """Chat-like client for the EAí Agent Gateway."""
    
    def __init__(self, base_url: str = "http://localhost:8000", user_id: str = None, bearer_token: str = None):
        """
        Initialize the chat client.
        
        Args:
            base_url: Base URL of the EAí Agent Gateway API
            user_id: Your user ID (will generate one if not provided)
            bearer_token: Optional Bearer token for API authentication
        """
        self.base_url = base_url.rstrip('/')
        self.session = requests.Session()
        self.user_id = user_id or f"user_{uuid.uuid4().hex[:8]}"
        self.conversation_history = []
        
        # Set up authentication if bearer token is provided
        if bearer_token:
            self.session.headers.update({
                'Authorization': f'Bearer {bearer_token}'
            })
        
    def _send_message(self, content: str, previous_message: str = None) -> Optional[str]:
        """Send a message and return the message ID."""
        url = f"{self.base_url}/api/v1/message/webhook/user"
        
        # Use the correct Python API schema
        payload = {
            "user_number": self.user_id,
            "message": content,
        }
        
        # Add previous message if provided
        if previous_message:
            payload["previous_message"] = previous_message
            
        try:
            response = self.session.post(url, json=payload, timeout=10)
            response.raise_for_status()
            data = response.json()
            return data.get("message_id")
        except Exception as e:
            print(f"❌ Error sending message: {e}")
            return None
    
    def _poll_response(self, message_id: str, timeout: int = 60) -> Optional[str]:
        """Poll for response and return the content."""
        url = f"{self.base_url}/api/v1/message/response"
        
        start_time = time.time()
        poll_interval = 1.0
        
        while time.time() - start_time < timeout:
            try:
                response = self.session.get(url, params={"message_id": message_id}, timeout=10)
                print(message_id, response.json())

                if response.status_code == 200:
                    data = response.json()
                    # Extract response from Python API format with structured data
                    response_data = data.get("data", {})
                    messages = response_data.get("messages", [])
                    
                    # Find the last assistant message
                    for msg in reversed(messages):
                        if msg.get("message_type") == "assistant_message" and msg.get("content"):
                            return msg["content"]
                    
                    return "✅ Message processed (no AI response found)"
                elif response.status_code == 202:
                    time.sleep(poll_interval)
                    continue
                else:
                    response.raise_for_status()
                    
            except Exception as e:
                time.sleep(poll_interval)
                continue
        
        return "⏰ Response timeout - the AI might still be thinking..."
    
    def send_message(self, message: str) -> str:
        """
        Send a message and wait for response.
        
        Args:
            message: Your message to send
            
        Returns:
            The AI's response
        """
        # Get previous message context (last AI response)
        previous_message = None
        if self.conversation_history:
            # Find the last assistant message
            for msg in reversed(self.conversation_history):
                if msg["role"] == "assistant":
                    previous_message = msg["content"]
                    break
        
        # Add user message to history
        self.conversation_history.append({
            "role": "user",
            "content": message,
            "timestamp": datetime.now()
        })
        
        # Send message with context
        message_id = self._send_message(message, previous_message)
        if not message_id:
            return "❌ Failed to send message to the gateway"
        
        # Show typing indicator
        self._show_typing_indicator()
        
        # Get response
        response = self._poll_response(message_id)
        
        # Add AI response to history
        self.conversation_history.append({
            "role": "assistant", 
            "content": response,
            "timestamp": datetime.now()
        })
        
        return response
    
    def _show_typing_indicator(self):
        """Show a typing indicator."""
        print("🤖 AI is thinking", end="", flush=True)
        for i in range(3):
            time.sleep(0.5)
            print(".", end="", flush=True)
        print(" ")
    
    def check_connection(self) -> bool:
        """Check if we can connect to the gateway."""
        try:
            response = self.session.get(f"{self.base_url}/health", timeout=5)
            return response.status_code == 200
        except:
            return False
    
    def show_conversation_history(self):
        """Display the conversation history."""
        if not self.conversation_history:
            print("💭 No conversation history yet")
            return
        
        print("\n" + "="*60)
        print("📜 CONVERSATION HISTORY")
        print("="*60)
        
        for msg in self.conversation_history:
            timestamp = msg["timestamp"].strftime("%H:%M:%S")
            role_icon = "👤" if msg["role"] == "user" else "🤖"
            role_name = "You" if msg["role"] == "user" else "AI"
            
            print(f"\n[{timestamp}] {role_icon} {role_name}:")
            print(f"  {msg['content']}")
        print()


def print_welcome(base_url: str):
    """Print welcome message."""
    print("=" * 60)
    print("💬 EAí Agent Gateway - Chat Interface")
    print("=" * 60)
    print("Welcome! You can now chat with the AI through the gateway.")
    print(f"🌐 Gateway URL: {base_url}")
    print("Type your messages and press Enter to send them.")
    print()
    print("💡 Commands:")
    print("  /help     - Show this help")
    print("  /history  - Show conversation history") 
    print("  /clear    - Clear conversation history")
    print("  /status   - Check gateway connection")
    print("  /quit     - Exit the chat")
    print("=" * 60)


def print_help():
    """Print help message."""
    print("\n💡 Available Commands:")
    print("  /help     - Show this help message")
    print("  /history  - Display your conversation history")
    print("  /clear    - Clear the conversation history")
    print("  /status   - Check if the gateway is responding")
    print("  /quit     - Exit the chat application")
    print("\n🗣️  Just type normally to chat with the AI!")


def parse_arguments():
    """Parse command line arguments."""
    parser = argparse.ArgumentParser(
        description="EAí Agent Gateway Chat Client",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  python chat_client.py
  python chat_client.py --url http://localhost:8080
  python chat_client.py --url https://api.example.com --token your-bearer-token
  python chat_client.py --user my-user-id --token your-token
  
Environment variables:
  GATEWAY_URL    - Base URL for the gateway (default: http://localhost:8000)
  BEARER_TOKEN   - Bearer token for authentication
  USER_ID        - User ID to use for the session
        """
    )
    
    parser.add_argument(
        '--url', '--base-url',
        type=str,
        default=os.getenv('GATEWAY_URL', 'http://localhost:8000'),
        help='Base URL of the EAí Agent Gateway API (default: http://localhost:8000)'
    )
    
    parser.add_argument(
        '--token', '--bearer-token',
        type=str,
        default=os.getenv('BEARER_TOKEN'),
        help='Bearer token for API authentication'
    )
    
    parser.add_argument(
        '--user', '--user-id',
        type=str,
        default=os.getenv('USER_ID'),
        help='User ID for the chat session (will generate random if not provided)'
    )
    
    parser.add_argument(
        '--interactive',
        action='store_true',
        help='Prompt for user ID interactively (ignores --user argument)'
    )
    
    return parser.parse_args()


def main():
    """Main chat loop."""
    # Parse command line arguments
    args = parse_arguments()
    
    print_welcome(args.url)
    
    # Get user ID
    user_id = args.user
    if args.interactive or not user_id:
        user_input = input("👤 Enter your user ID (or press Enter for random): ").strip()
        if user_input:
            user_id = user_input
        elif not user_id:
            user_id = f"user_{uuid.uuid4().hex[:8]}"
            print(f"   Generated user ID: {user_id}")
    
    # Show authentication status
    auth_status = "🔓 No authentication" if not args.token else "🔐 Using Bearer token"
    print(f"🔑 Auth: {auth_status}")
    
    # Initialize client
    client = ChatClient(base_url=args.url, user_id=user_id, bearer_token=args.token)
    
    # Check connection
    print("\n🔍 Checking gateway connection...")
    if not client.check_connection():
        print(f"⚠️  Warning: Cannot connect to gateway at {args.url}")
        print("   Make sure the gateway is running and try again.")
        response = input("   Continue anyway? (y/N): ").strip().lower()
        if response != 'y':
            print("👋 Goodbye!")
            return
    else:
        print("✅ Connected to gateway successfully!")
    
    print(f"\n💬 Chat started! You are: {user_id}")
    print("🤖 AI agent is ready. Start chatting!\n")
    
    # Main chat loop
    while True:
        try:
            # Get user input
            user_input = input("👤 You: ").strip()
            
            if not user_input:
                continue
            
            # Handle commands
            if user_input.startswith('/'):
                command = user_input.lower()
                
                if command == '/quit' or command == '/exit':
                    print("👋 Thanks for chatting! Goodbye!")
                    break
                
                elif command == '/help':
                    print_help()
                    continue
                
                elif command == '/history':
                    client.show_conversation_history()
                    continue
                
                elif command == '/clear':
                    client.conversation_history = []
                    print("🗑️  Conversation history cleared!")
                    continue
                
                elif command == '/status':
                    print("🔍 Checking gateway status...")
                    if client.check_connection():
                        print("✅ Gateway is responding")
                    else:
                        print("❌ Gateway is not responding")
                    continue
                
                else:
                    print(f"❓ Unknown command: {user_input}")
                    print("   Type /help to see available commands")
                    continue
            
            # Send regular message
            print() # Add some space before AI response
            response = client.send_message(user_input)
            print(f"🤖 AI: {response}\n")
            
        except KeyboardInterrupt:
            print("\n\n⏹️  Chat interrupted")
            response = input("Do you want to quit? (y/N): ").strip().lower()
            if response == 'y':
                print("👋 Goodbye!")
                break
            else:
                print("💬 Continuing chat...\n")
                continue
                
        except EOFError:
            print("\n👋 Goodbye!")
            break
            
        except Exception as e:
            print(f"\n❌ Unexpected error: {e}")
            print("💡 Continuing chat... (type /quit to exit)")


if __name__ == "__main__":
    main()