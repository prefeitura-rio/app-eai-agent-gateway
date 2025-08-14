#!/usr/bin/env python3
"""
Testes para as correções de event loop
Verifica se as funções safe_async_to_sync estão funcionando corretamente
"""

import asyncio
import sys
import traceback
import time
import os
from concurrent.futures import ThreadPoolExecutor

# Adicionar o diretório pai ao path para importar src
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from src.services.letta_service import safe_async_to_sync as letta_safe_async_to_sync
from src.services.google_agent_engine_service import safe_async_to_sync as google_safe_async_to_sync


async def test_async_function(value: str, delay: float = 0.1):
    """Função async simples para testar"""
    await asyncio.sleep(delay)
    return f"processed_{value}"


async def test_async_function_with_error():
    """Função async que gera erro"""
    await asyncio.sleep(0.05)
    raise ValueError("Test error from async function")


def test_normal_event_loop():
    """Testa safe_async_to_sync em condições normais"""
    print("🧪 Testando safe_async_to_sync em condições normais...")
    
    try:
        # Teste com Letta safe_async_to_sync
        result1 = letta_safe_async_to_sync(test_async_function, "letta_test")
        assert result1 == "processed_letta_test", f"Esperado 'processed_letta_test', recebido '{result1}'"
        
        # Teste com Google safe_async_to_sync  
        result2 = google_safe_async_to_sync(test_async_function, "google_test")
        assert result2 == "processed_google_test", f"Esperado 'processed_google_test', recebido '{result2}'"
        
        print("✅ safe_async_to_sync funcionando em condições normais")
        return True
        
    except Exception as e:
        print(f"❌ Erro em condições normais: {e}")
        traceback.print_exc()
        return False


def test_closed_event_loop():
    """Testa safe_async_to_sync quando o event loop está fechado"""
    print("🧪 Testando safe_async_to_sync com event loop fechado...")
    
    def run_in_thread():
        """Simula condição de Celery worker onde event loop pode estar fechado"""
        try:
            # Primeiro, criar e fechar um loop para simular estado problemático
            loop = asyncio.new_event_loop()
            asyncio.set_event_loop(loop)
            loop.close()  # Simula loop fechado
            
            # Agora tentar usar safe_async_to_sync
            result1 = letta_safe_async_to_sync(test_async_function, "letta_closed_test")
            assert result1 == "processed_letta_closed_test", f"Letta: esperado 'processed_letta_closed_test', recebido '{result1}'"
            
            result2 = google_safe_async_to_sync(test_async_function, "google_closed_test") 
            assert result2 == "processed_google_closed_test", f"Google: esperado 'processed_google_closed_test', recebido '{result2}'"
            
            return True
            
        except Exception as e:
            print(f"❌ Erro em thread com loop fechado: {e}")
            traceback.print_exc()
            return False
    
    try:
        # Executar em thread separada para simular ambiente Celery
        with ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(run_in_thread)
            success = future.result(timeout=10)  # 10 segundos timeout
            
        if success:
            print("✅ safe_async_to_sync funcionando com event loop fechado")
            return True
        else:
            print("❌ Falha no teste com event loop fechado")
            return False
            
    except Exception as e:
        print(f"❌ Erro no teste de event loop fechado: {e}")
        traceback.print_exc()
        return False


def test_async_function_with_exception():
    """Testa se exceções são propagadas corretamente"""
    print("🧪 Testando propagação de exceções...")
    
    try:
        # Deve propagar a exceção da função async
        try:
            letta_safe_async_to_sync(test_async_function_with_error)
            assert False, "Deveria ter gerado exceção"
        except ValueError as e:
            assert "Test error from async function" in str(e), "Exceção não foi propagada corretamente"
        
        try:
            google_safe_async_to_sync(test_async_function_with_error)
            assert False, "Deveria ter gerado exceção"
        except ValueError as e:
            assert "Test error from async function" in str(e), "Exceção não foi propagada corretamente"
        
        print("✅ Exceções propagadas corretamente")
        return True
        
    except Exception as e:
        print(f"❌ Erro no teste de exceções: {e}")
        traceback.print_exc()
        return False


def test_concurrent_access():
    """Testa acesso concurrent à safe_async_to_sync"""
    print("🧪 Testando acesso concurrent...")
    
    def worker(worker_id):
        """Worker function para teste concurrent"""
        try:
            results = []
            for i in range(3):
                result1 = letta_safe_async_to_sync(test_async_function, f"worker_{worker_id}_letta_{i}")
                result2 = google_safe_async_to_sync(test_async_function, f"worker_{worker_id}_google_{i}")
                results.extend([result1, result2])
            return results
        except Exception as e:
            print(f"❌ Erro no worker {worker_id}: {e}")
            return None
    
    try:
        # Executar múltiplos workers concorrentemente
        with ThreadPoolExecutor(max_workers=4) as executor:
            futures = [executor.submit(worker, i) for i in range(4)]
            
            all_results = []
            for future in futures:
                result = future.result(timeout=15)
                if result is None:
                    return False
                all_results.extend(result)
        
        # Verificar se todos os resultados estão corretos
        expected_count = 4 * 3 * 2  # 4 workers * 3 iterations * 2 providers
        assert len(all_results) == expected_count, f"Esperado {expected_count} resultados, recebido {len(all_results)}"
        
        # Verificar se todos os resultados são válidos
        for result in all_results:
            assert result.startswith("processed_worker_"), f"Resultado inválido: {result}"
        
        print("✅ Acesso concurrent funcionando")
        return True
        
    except Exception as e:
        print(f"❌ Erro no teste concurrent: {e}")
        traceback.print_exc()
        return False


def test_performance():
    """Testa performance básica da safe_async_to_sync"""
    print("🧪 Testando performance...")
    
    try:
        start_time = time.time()
        
        # Executar múltiplas chamadas
        for i in range(10):
            letta_safe_async_to_sync(test_async_function, f"perf_test_{i}", 0.01)  # delay menor
            google_safe_async_to_sync(test_async_function, f"perf_test_{i}", 0.01)
        
        elapsed = time.time() - start_time
        
        # Deve completar em tempo razoável (menos de 5 segundos para 20 operações)
        assert elapsed < 5.0, f"Performance ruim: {elapsed:.2f}s para 20 operações"
        
        print(f"✅ Performance OK: {elapsed:.2f}s para 20 operações")
        return True
        
    except Exception as e:
        print(f"❌ Erro no teste de performance: {e}")
        traceback.print_exc()
        return False


def run_event_loop_tests():
    """Executa todos os testes de event loop"""
    print("🚀 Executando testes de Event Loop Fixes...\n")
    
    tests = [
        test_normal_event_loop,
        test_closed_event_loop,
        test_async_function_with_exception,
        test_concurrent_access,
        test_performance
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
    print(f"📊 Resultados dos testes Event Loop:")
    print(f"✅ Passou: {passed}/{total}")
    print(f"❌ Falhou: {total - passed}/{total}")
    
    if passed == total:
        print("🎉 Todos os testes de event loop passaram!")
        return True
    else:
        print("💥 Alguns testes de event loop falharam!")
        return False


if __name__ == "__main__":
    success = run_event_loop_tests()
    sys.exit(0 if success else 1)