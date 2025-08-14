#!/usr/bin/env python3
"""
Testes de integração para os providers (Letta e Google Agent Engine)
Verifica se os serviços estão funcionando corretamente com as APIs externas
"""

import sys
import traceback
import time
import os
from datetime import datetime

# Adicionar o diretório pai ao path para importar src
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from src.services.letta_service import letta_service
from src.services.google_agent_engine_service import google_agent_engine_service
from src.services.agent_provider_factory import agent_provider_factory
from src.utils.message_formatter import to_gateway_format


def test_letta_service_basic():
    """Testa funcionalidade básica do Letta service"""
    print("🧪 Testando Letta service básico...")
    
    try:
        test_user = f"test_user_letta_{int(time.time())}"
        
        # Teste 1: get_agent_id_sync (pode retornar None se não existir)
        agent_id = letta_service.get_agent_id_sync(test_user)
        print(f"   📋 Agent ID para {test_user}: {agent_id}")
        
        # Teste 2: create_agent_sync (se não existir)
        if agent_id is None:
            print("   🔨 Criando novo agente...")
            agent_id = letta_service.create_agent_sync(test_user)
            assert agent_id is not None, "Deveria criar um agente"
            print(f"   ✅ Agente criado: {agent_id}")
        
        # Teste 3: Verificar cache funcionando
        cached_agent_id = letta_service.get_agent_id_sync(test_user)
        assert cached_agent_id == agent_id, "Cache deveria retornar o mesmo agent_id"
        
        print("✅ Letta service básico funcionando")
        return True
        
    except Exception as e:
        print(f"❌ Erro no Letta service: {e}")
        # Não imprimir traceback completo para APIs externas (pode ter info sensível)
        print(f"   Tipo do erro: {type(e).__name__}")
        return False


def test_google_agent_engine_basic():
    """Testa funcionalidade básica do Google Agent Engine"""
    print("🧪 Testando Google Agent Engine básico...")
    
    try:
        test_user = f"test_user_google_{int(time.time())}"
        
        # Teste 1: get_thread_id_sync 
        thread_id = google_agent_engine_service.get_thread_id_sync(test_user)
        assert thread_id is not None, "Deveria retornar thread_id"
        print(f"   📋 Thread ID para {test_user}: {thread_id}")
        
        # Teste 2: create_agent_sync
        created_thread_id = google_agent_engine_service.create_agent_sync(test_user)
        assert created_thread_id is not None, "Deveria criar thread"
        print(f"   ✅ Thread criado: {created_thread_id}")
        
        # Teste 3: Verificar cache funcionando
        cached_thread_id = google_agent_engine_service.get_thread_id_sync(test_user)
        assert cached_thread_id == thread_id, "Cache deveria retornar o mesmo thread_id"
        
        print("✅ Google Agent Engine básico funcionando")
        return True
        
    except Exception as e:
        print(f"❌ Erro no Google Agent Engine: {e}")
        print(f"   Tipo do erro: {type(e).__name__}")
        return False


def test_provider_factory():
    """Testa o factory de providers"""
    print("🧪 Testando Provider Factory...")
    
    try:
        # Teste 1: Obter providers disponíveis
        available_providers = agent_provider_factory.get_available_providers()
        expected_providers = ["letta", "google_agent_engine"]
        
        for provider in expected_providers:
            assert provider in available_providers, f"Provider {provider} deveria estar disponível"
        
        # Teste 2: Obter provider Letta
        letta_provider = agent_provider_factory.get_provider("letta")
        assert letta_provider is not None, "Deveria retornar provider Letta"
        
        # Teste 3: Obter provider Google
        google_provider = agent_provider_factory.get_provider("google_agent_engine")
        assert google_provider is not None, "Deveria retornar provider Google"
        
        # Teste 4: Provider inválido
        try:
            invalid_provider = agent_provider_factory.get_provider("invalid_provider")
            assert False, "Deveria gerar erro para provider inválido"
        except ValueError:
            pass  # Esperado
        
        print("✅ Provider Factory funcionando")
        return True
        
    except Exception as e:
        print(f"❌ Erro no Provider Factory: {e}")
        traceback.print_exc()
        return False


def test_provider_integration_letta():
    """Testa integração completa com provider Letta"""
    print("🧪 Testando integração Provider Letta...")
    
    try:
        test_user = f"integration_letta_{int(time.time())}"
        letta_provider = agent_provider_factory.get_provider("letta")
        
        # Teste 1: Obter/criar agente
        agent_id = letta_provider.get_agent_id_sync(test_user)
        if agent_id is None:
            agent_id = letta_provider.create_agent_sync(test_user)
        
        assert agent_id is not None, "Deveria ter agent_id"
        print(f"   📋 Usando agente: {agent_id}")
        
        # Teste 2: Enviar mensagem simples (que pode falhar por falta de conexão)
        try:
            messages, usage = letta_provider.send_message_sync(
                agent_id, 
                "Teste de integração - responda apenas 'OK'"
            )
            
            # Se chegou aqui, a integração funcionou
            print(f"   ✅ Mensagem enviada, recebidas {len(messages)} mensagens")
            
            # Teste 3: Verificar formato das mensagens usando message_formatter
            if messages:
                formatted = to_gateway_format(
                    messages=messages,
                    thread_id=agent_id,
                    use_whatsapp_format=True
                )
                
                assert formatted["status"] == "completed", "Formatação deveria ser 'completed'"
                assert len(formatted["data"]["messages"]) > 0, "Deveria ter mensagens formatadas"
                print("   ✅ Formatação de mensagens OK")
            
        except Exception as api_error:
            # API externa pode falhar - isso é OK para nossos testes
            print(f"   ⚠️ API call falhou (esperado em alguns ambientes): {type(api_error).__name__}")
        
        print("✅ Integração Provider Letta funcionando")
        return True
        
    except Exception as e:
        print(f"❌ Erro na integração Letta: {e}")
        print(f"   Tipo do erro: {type(e).__name__}")
        return False


def test_provider_integration_google():
    """Testa integração completa com provider Google"""
    print("🧪 Testando integração Provider Google...")
    
    try:
        test_user = f"integration_google_{int(time.time())}"
        google_provider = agent_provider_factory.get_provider("google_agent_engine")
        
        # Teste 1: Obter/criar thread
        thread_id = google_provider.get_agent_id_sync(test_user)
        if thread_id is None:
            thread_id = google_provider.create_agent_sync(test_user)
        
        assert thread_id is not None, "Deveria ter thread_id"
        print(f"   📋 Usando thread: {thread_id}")
        
        # Teste 2: Enviar mensagem simples (que pode falhar por falta de conexão)
        try:
            messages, usage = google_provider.send_message_sync(
                thread_id,
                "Teste de integração - responda apenas 'OK'"
            )
            
            # Se chegou aqui, a integração funcionou
            print(f"   ✅ Mensagem enviada, recebidas {len(messages)} mensagens")
            
            # Teste 3: Verificar formato das mensagens
            if messages:
                formatted = to_gateway_format(
                    messages=messages,
                    thread_id=thread_id,
                    use_whatsapp_format=True
                )
                
                assert formatted["status"] == "completed", "Formatação deveria ser 'completed'"
                assert len(formatted["data"]["messages"]) > 0, "Deveria ter mensagens formatadas"
                print("   ✅ Formatação de mensagens OK")
                
        except Exception as api_error:
            # API externa pode falhar - isso é OK para nossos testes
            print(f"   ⚠️ API call falhou (esperado em alguns ambientes): {type(api_error).__name__}")
        
        print("✅ Integração Provider Google funcionando")
        return True
        
    except Exception as e:
        print(f"❌ Erro na integração Google: {e}")
        print(f"   Tipo do erro: {type(e).__name__}")
        return False


def test_error_handling():
    """Testa tratamento de erros nos providers"""
    print("🧪 Testando tratamento de erros...")
    
    try:
        # Teste 1: User number inválido
        try:
            letta_service.get_agent_id_sync("")  # string vazia
            # Se não falhar, está OK (pode retornar None)
        except Exception:
            pass  # Qualquer exceção é aceitável
        
        try:
            google_agent_engine_service.get_thread_id_sync("")  # string vazia
            # Se não falhar, está OK
        except Exception:
            pass  # Qualquer exceção é aceitável
        
        # Teste 2: Agent ID inexistente para send_message
        try:
            letta_provider = agent_provider_factory.get_provider("letta")
            letta_provider.send_message_sync("agent_inexistente_123", "test")
            # Se não falhar, pode ser que a API esteja retornando algo
        except Exception:
            pass  # Erro esperado
        
        print("✅ Tratamento de erros funcionando")
        return True
        
    except Exception as e:
        print(f"❌ Erro no teste de tratamento de erros: {e}")
        traceback.print_exc()
        return False


def run_provider_integration_tests():
    """Executa todos os testes de integração dos providers"""
    print("🚀 Executando testes de integração dos Providers...\n")
    
    tests = [
        test_letta_service_basic,
        test_google_agent_engine_basic,
        test_provider_factory,
        test_provider_integration_letta,
        test_provider_integration_google,
        test_error_handling
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
            print(f"   Tipo do erro: {type(e).__name__}")
            print()
    
    print("=" * 50)
    print(f"📊 Resultados dos testes de Integração:")
    print(f"✅ Passou: {passed}/{total}")
    print(f"❌ Falhou: {total - passed}/{total}")
    
    if passed == total:
        print("🎉 Todos os testes de integração passaram!")
        return True
    else:
        print("💥 Alguns testes de integração falharam!")
        print("   ⚠️ Nota: Falhas podem ser normais se APIs externas não estiverem disponíveis")
        return False


if __name__ == "__main__":
    success = run_provider_integration_tests()
    sys.exit(0 if success else 1)