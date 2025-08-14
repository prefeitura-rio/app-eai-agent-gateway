#!/usr/bin/env python3
"""
Teste para verificar se a serialização de objetos datetime está funcionando
"""

import sys
import os
from datetime import datetime, timezone
import json

# Adicionar o diretório pai ao path para importar src
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from src.services.redis_service import safe_json_dumps, DateTimeJSONEncoder


def test_datetime_serialization():
    """Testa se objetos datetime são serializados corretamente"""
    print("🧪 Testando serialização de datetime...")
    
    # Dados de teste com datetime
    test_data = {
        "message_type": "assistant_message",
        "content": "Teste",
        "date": datetime.now(timezone.utc),
        "processed_at": datetime(2024, 1, 1, 12, 0, 0, tzinfo=timezone.utc),
        "nested": {
            "timestamp": datetime.now(timezone.utc),
            "value": "test"
        },
        "list_with_datetime": [
            datetime.now(timezone.utc),
            "string",
            123
        ]
    }
    
    try:
        # Tentar serializar usando o safe_json_dumps
        result = safe_json_dumps(test_data)
        
        # Verificar se é uma string válida
        assert isinstance(result, str), "Resultado deve ser string"
        
        # Verificar se pode ser parseado de volta
        parsed = json.loads(result)
        assert isinstance(parsed, dict), "Resultado parseado deve ser dict"
        
        # Verificar se os datetime foram convertidos para strings
        assert isinstance(parsed["date"], str), "datetime deve virar string"
        assert isinstance(parsed["processed_at"], str), "datetime deve virar string"
        assert isinstance(parsed["nested"]["timestamp"], str), "datetime aninhado deve virar string"
        assert isinstance(parsed["list_with_datetime"][0], str), "datetime em lista deve virar string"
        
        # Verificar formato ISO
        assert "T" in parsed["date"], "datetime deve estar em formato ISO"
        assert parsed["processed_at"].startswith("2024-01-01T12:00:00"), "datetime específico deve estar correto"
        
        print("✅ Serialização de datetime funcionando")
        return True
        
    except Exception as e:
        print(f"❌ Erro na serialização de datetime: {e}")
        return False


def test_letta_message_format():
    """Testa serialização de formato similar ao retornado pelo Letta"""
    print("🧪 Testando formato de mensagem do Letta...")
    
    # Simular dados do Letta com datetime
    letta_data = {
        "status": "completed",
        "data": {
            "messages": [
                {
                    "id": "msg-123",
                    "message_type": "user_message",
                    "content": "Teste",
                    "date": datetime.now(timezone.utc),
                    "session_id": None,
                    "time_since_last_message": None
                },
                {
                    "id": "msg-124", 
                    "message_type": "assistant_message",
                    "content": "Resposta",
                    "date": datetime.now(timezone.utc),
                    "model_name": "test-model",
                    "usage_metadata": {
                        "prompt_token_count": 10,
                        "candidates_token_count": 20,
                        "total_token_count": 30
                    }
                },
                {
                    "message_type": "usage_statistics",
                    "processed_at": datetime.now(timezone.utc),
                    "total_tokens": 30,
                    "status": "done"
                }
            ]
        }
    }
    
    try:
        # Serializar com safe_json_dumps
        result = safe_json_dumps(letta_data)
        
        # Parse de volta
        parsed = json.loads(result)
        
        # Verificar estrutura
        assert parsed["status"] == "completed", "Status preservado"
        assert len(parsed["data"]["messages"]) == 3, "Todas as mensagens preservadas"
        
        # Verificar que todos os datetime foram convertidos
        for msg in parsed["data"]["messages"]:
            if "date" in msg:
                assert isinstance(msg["date"], str), f"datetime em {msg['message_type']} deve ser string"
            if "processed_at" in msg:
                assert isinstance(msg["processed_at"], str), f"processed_at em {msg['message_type']} deve ser string"
        
        print("✅ Formato de mensagem Letta serializado corretamente")
        return True
        
    except Exception as e:
        print(f"❌ Erro na serialização de formato Letta: {e}")
        return False


def test_normal_json_compatibility():
    """Testa se dados normais ainda funcionam corretamente"""
    print("🧪 Testando compatibilidade com JSON normal...")
    
    normal_data = {
        "string": "test",
        "number": 123,
        "boolean": True,
        "null": None,
        "list": [1, 2, 3],
        "nested": {
            "key": "value"
        }
    }
    
    try:
        # Serializar
        result = safe_json_dumps(normal_data)
        
        # Parse de volta
        parsed = json.loads(result)
        
        # Verificar que é idêntico
        assert parsed == normal_data, "Dados normais devem permanecer iguais"
        
        print("✅ Compatibilidade com JSON normal mantida")
        return True
        
    except Exception as e:
        print(f"❌ Erro na compatibilidade JSON normal: {e}")
        return False


def run_datetime_serialization_tests():
    """Executa todos os testes de serialização datetime"""
    print("🚀 Executando testes de serialização datetime...\n")
    
    tests = [
        test_datetime_serialization,
        test_letta_message_format,
        test_normal_json_compatibility
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
            print()
    
    print("=" * 50)
    print(f"📊 Resultados dos testes de serialização datetime:")
    print(f"✅ Passou: {passed}/{total}")
    print(f"❌ Falhou: {total - passed}/{total}")
    
    if passed == total:
        print("🎉 Todos os testes de serialização passaram!")
        return True
    else:
        print("💥 Alguns testes de serialização falharam!")
        return False


if __name__ == "__main__":
    success = run_datetime_serialization_tests()
    sys.exit(0 if success else 1)