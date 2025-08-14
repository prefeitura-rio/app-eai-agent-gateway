#!/usr/bin/env python3
"""
Testes para o message_formatter.py
Verifica se a formatação de mensagens está funcionando corretamente
"""

import json
import sys
import traceback
import os
from datetime import datetime, timezone
from typing import Dict, Any, List

# Adicionar o diretório pai ao path para importar src
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# Import the formatter
from src.utils.message_formatter import to_gateway_format, LangGraphMessageFormatter


def test_google_agent_engine_format():
    """Testa formatação de mensagens do Google Agent Engine"""
    print("🧪 Testando formatação Google Agent Engine...")
    
    google_messages = [
        {
            "type": "human",
            "content": "Me fale sobre **Python** e como usar *listas*"
        },
        {
            "type": "ai", 
            "content": "**Python** é excelente para:\n\n- Trabalhar com *listas*\n- ~~Programação complexa~~ código simples\n\n```python\nlista = [1, 2, 3]\nprint(lista)\n```",
            "usage_metadata": {
                "prompt_token_count": 25,
                "candidates_token_count": 75,
                "total_token_count": 100
            }
        }
    ]
    
    try:
        result = to_gateway_format(
            messages=google_messages,
            thread_id="test-google-123",
            use_whatsapp_format=True
        )
        
        # Verificar estrutura básica
        assert result["status"] == "completed", "Status deve ser 'completed'"
        assert "data" in result, "Deve ter campo 'data'"
        assert "messages" in result["data"], "Deve ter campo 'messages'"
        
        messages = result["data"]["messages"]
        
        # Deve ter pelo menos 3 mensagens: user, assistant, usage_statistics
        assert len(messages) >= 3, f"Deve ter pelo menos 3 mensagens, encontrou {len(messages)}"
        
        # Verificar mensagem do usuário
        user_msg = next((m for m in messages if m.get("message_type") == "user_message"), None)
        assert user_msg is not None, "Deve ter mensagem do usuário"
        assert "Python" in user_msg["content"], "Conteúdo do usuário deve estar preservado"
        
        # Verificar mensagem do assistente (com formatação WhatsApp)
        assistant_msg = next((m for m in messages if m.get("message_type") == "assistant_message"), None)
        assert assistant_msg is not None, "Deve ter mensagem do assistente"
        content = assistant_msg["content"]
        
        # Verificar conversão Markdown -> WhatsApp
        assert "*Python*" in content, "**bold** deve virar *bold*"
        assert "_listas_" in content, "*italic* deve virar _italic_"
        assert "~Programação complexa~" in content, "~~strike~~ deve virar ~strike~"
        assert "```python" in content, "Código deve ser preservado"
        
        # Verificar usage statistics
        usage_msg = next((m for m in messages if m.get("message_type") == "usage_statistics"), None)
        assert usage_msg is not None, "Deve ter estatísticas de uso"
        assert usage_msg["total_tokens"] >= 0, "Deve ter contagem de tokens"
        
        print("✅ Formatação Google Agent Engine OK")
        return True
        
    except Exception as e:
        print(f"❌ Erro na formatação Google Agent Engine: {e}")
        traceback.print_exc()
        return False


def test_letta_format():
    """Testa formatação de mensagens do Letta"""
    print("🧪 Testando formatação Letta...")
    
    letta_messages = [
        {
            "message_type": "user_message",
            "content": "Como usar **markdown** com _itálico_?",
            "date": datetime.now(timezone.utc).isoformat()
        },
        {
            "message_type": "assistant_message", 
            "content": "Use **negrito** para ênfase e _itálico_ para destaque:\n\n- Lista com **items**\n- ~~Riscado~~ quando necessário",
            "date": datetime.now(timezone.utc).isoformat()
        },
        {
            "message_type": "tool_call_message",
            "tool_call": {
                "name": "search_web",
                "arguments": {"query": "markdown tutorial"},
                "tool_call_id": "call_123"
            },
            "date": datetime.now(timezone.utc).isoformat()
        },
        {
            "message_type": "tool_return_message",
            "content": "Encontrei 10 resultados sobre markdown",
            "name": "search_web",
            "tool_call_id": "call_123",
            "is_err": False,
            "date": datetime.now(timezone.utc).isoformat()
        }
    ]
    
    try:
        result = to_gateway_format(
            messages=letta_messages,
            thread_id="test-letta-456",
            use_whatsapp_format=True
        )
        
        # Verificar estrutura básica
        assert result["status"] == "completed", "Status deve ser 'completed'"
        messages = result["data"]["messages"]
        
        # Verificar tipos de mensagem esperados
        message_types = [m.get("message_type") for m in messages]
        expected_types = ["user_message", "assistant_message", "tool_call_message", "tool_return_message", "usage_statistics"]
        
        for expected_type in expected_types:
            assert expected_type in message_types, f"Deve ter mensagem do tipo {expected_type}"
        
        # Verificar mensagem do assistente com formatação
        assistant_msg = next((m for m in messages if m.get("message_type") == "assistant_message"), None)
        content = assistant_msg["content"]
        assert "*negrito*" in content, "**bold** deve virar *bold*"
        assert "_itálico_" in content, "_italic_ deve permanecer _italic_"
        
        # Verificar tool call
        tool_call_msg = next((m for m in messages if m.get("message_type") == "tool_call_message"), None)
        assert tool_call_msg["tool_call"]["name"] == "search_web", "Tool call deve estar preservado"
        
        # Verificar tool return
        tool_return_msg = next((m for m in messages if m.get("message_type") == "tool_return_message"), None)
        assert "markdown" in tool_return_msg["tool_return"], "Tool return deve estar preservado"
        
        print("✅ Formatação Letta OK")
        return True
        
    except Exception as e:
        print(f"❌ Erro na formatação Letta: {e}")
        traceback.print_exc()
        return False


def test_langchain_messages():
    """Testa formatação de mensagens LangChain serializadas"""
    print("🧪 Testando formatação LangChain...")
    
    # Simular mensagens LangChain serializadas
    langchain_messages = [
        {
            "kwargs": {
                "type": "human",
                "content": "Test message with **formatting**",
                "additional_kwargs": {
                    "timestamp": datetime.now(timezone.utc).isoformat()
                }
            }
        },
        {
            "kwargs": {
                "type": "ai",
                "content": "Response with *italic* and **bold** text",
                "response_metadata": {
                    "model_name": "test-model",
                    "finish_reason": "stop",
                    "usage_metadata": {
                        "prompt_token_count": 10,
                        "candidates_token_count": 20,
                        "total_token_count": 30
                    }
                }
            }
        }
    ]
    
    try:
        result = to_gateway_format(
            messages=langchain_messages,
            thread_id="test-langchain-789",
            use_whatsapp_format=True
        )
        
        # Verificar estrutura básica
        assert result["status"] == "completed"
        messages = result["data"]["messages"]
        
        # Verificar que as mensagens foram processadas
        user_msg = next((m for m in messages if m.get("message_type") == "user_message"), None)
        assert user_msg is not None, "Deve ter mensagem do usuário"
        
        assistant_msg = next((m for m in messages if m.get("message_type") == "assistant_message"), None)
        assert assistant_msg is not None, "Deve ter mensagem do assistente"
        
        # Verificar formatação
        content = assistant_msg["content"]
        assert "_italic_" in content, "*italic* deve virar _italic_"
        assert "*bold*" in content, "**bold** deve virar *bold*"
        
        # Verificar metadados
        assert assistant_msg["model_name"] == "test-model", "Model name deve estar preservado"
        assert assistant_msg["usage_metadata"]["total_token_count"] == 30, "Usage metadata deve estar preservado"
        
        print("✅ Formatação LangChain OK")
        return True
        
    except Exception as e:
        print(f"❌ Erro na formatação LangChain: {e}")
        traceback.print_exc()
        return False


def test_whatsapp_formatting_disabled():
    """Testa quando formatação WhatsApp está desabilitada"""
    print("🧪 Testando formatação WhatsApp desabilitada...")
    
    messages = [
        {
            "type": "human",
            "content": "Test message"
        },
        {
            "type": "ai",
            "content": "Response with **bold** and *italic*"
        }
    ]
    
    try:
        result = to_gateway_format(
            messages=messages,
            thread_id="test-no-whatsapp",
            use_whatsapp_format=False  # Desabilitado
        )
        
        assistant_msg = next((m for m in result["data"]["messages"] if m.get("message_type") == "assistant_message"), None)
        content = assistant_msg["content"]
        
        # Markdown deve estar preservado (não convertido)
        assert "**bold**" in content, "**bold** deve permanecer quando WhatsApp está desabilitado"
        assert "*italic*" in content, "*italic* deve permanecer quando WhatsApp está desabilitado"
        
        print("✅ Formatação WhatsApp desabilitada OK")
        return True
        
    except Exception as e:
        print(f"❌ Erro quando WhatsApp desabilitado: {e}")
        traceback.print_exc()
        return False


def run_message_formatter_tests():
    """Executa todos os testes do message formatter"""
    print("🚀 Executando testes do Message Formatter...\n")
    
    tests = [
        test_google_agent_engine_format,
        test_letta_format, 
        test_langchain_messages,
        test_whatsapp_formatting_disabled
    ]
    
    passed = 0
    total = len(tests)
    
    for test in tests:
        try:
            if test():
                passed += 1
            print()
        except Exception as e:
            print(f"❌ Erro inesperado no teste: {e}")
            traceback.print_exc()
            print()
    
    print("=" * 50)
    print(f"📊 Resultados dos testes Message Formatter:")
    print(f"✅ Passou: {passed}/{total}")
    print(f"❌ Falhou: {total - passed}/{total}")
    
    if passed == total:
        print("🎉 Todos os testes passaram!")
        return True
    else:
        print("💥 Alguns testes falharam!")
        return False


if __name__ == "__main__":
    success = run_message_formatter_tests()
    sys.exit(0 if success else 1)