#!/usr/bin/env python3
"""
Script principal para executar todos os testes do sistema
Verifica se os serviços estão funcionando corretamente
"""

import sys
import time
import argparse
from datetime import datetime
from typing import List, Tuple
import os

# Adicionar o diretório pai ao path para importar src
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# Import test modules
from tests.test_message_formatter import run_message_formatter_tests
from tests.test_event_loop_fixes import run_event_loop_tests  
from tests.test_providers_integration import run_provider_integration_tests


def print_header(title: str):
    """Imprime cabeçalho formatado"""
    print("\n" + "=" * 60)
    print(f" {title}")
    print("=" * 60)


def print_summary(results: List[Tuple[str, bool, float]]):
    """Imprime resumo dos resultados"""
    print_header("📊 RESUMO GERAL DOS TESTES")
    
    total_tests = len(results)
    passed_tests = sum(1 for _, passed, _ in results if passed)
    total_time = sum(time for _, _, time in results)
    
    print(f"🕒 Tempo total: {total_time:.2f}s")
    print(f"📝 Total de suítes: {total_tests}")
    print(f"✅ Suítes passou: {passed_tests}")
    print(f"❌ Suítes falhou: {total_tests - passed_tests}")
    print(f"📈 Taxa de sucesso: {(passed_tests/total_tests)*100:.1f}%\n")
    
    # Detalhar resultados
    for test_name, passed, duration in results:
        status = "✅ PASSOU" if passed else "❌ FALHOU" 
        print(f"{status:12} {test_name:30} ({duration:.2f}s)")
    
    print("\n" + "=" * 60)
    
    if passed_tests == total_tests:
        print("🎉 TODOS OS TESTES PASSARAM!")
        print("   O sistema está funcionando corretamente.")
    else:
        print("💥 ALGUNS TESTES FALHARAM!")
        print("   Verifique os logs acima para detalhes dos problemas.")
    
    print("=" * 60)


def run_test_suite(name: str, test_function, skip_on_error: bool = False):
    """
    Executa uma suíte de testes com tratamento de erros
    
    Args:
        name: Nome da suíte de testes
        test_function: Função que executa os testes
        skip_on_error: Se True, continua mesmo se houver erro
    
    Returns:
        Tuple[bool, float]: (sucesso, tempo_execucao)
    """
    print_header(f"🧪 EXECUTANDO: {name}")
    start_time = time.time()
    
    try:
        success = test_function()
        duration = time.time() - start_time
        
        if success:
            print(f"\n✅ {name} - TODOS OS TESTES PASSARAM ({duration:.2f}s)")
        else:
            print(f"\n❌ {name} - ALGUNS TESTES FALHARAM ({duration:.2f}s)")
        
        return success, duration
        
    except Exception as e:
        duration = time.time() - start_time
        print(f"\n💥 {name} - ERRO FATAL: {e}")
        print(f"   Tempo antes do erro: {duration:.2f}s")
        
        if skip_on_error:
            print("   ⚠️ Continuando execução (skip_on_error=True)")
            return False, duration
        else:
            print("   🛑 Interrompendo execução")
            raise


def main():
    """Função principal"""
    parser = argparse.ArgumentParser(
        description="Executa testes do EAí Gateway",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Exemplos de uso:
  python run_all_tests.py                    # Executa todos os testes
  python run_all_tests.py --skip-integration # Pula testes de integração
  python run_all_tests.py --continue-on-error # Continua mesmo com erros
  python run_all_tests.py --quick            # Executa apenas testes rápidos
        """
    )
    
    parser.add_argument(
        "--skip-integration", 
        action="store_true",
        help="Pula testes de integração com APIs externas"
    )
    
    parser.add_argument(
        "--continue-on-error",
        action="store_true", 
        help="Continua execução mesmo se um teste falhar"
    )
    
    parser.add_argument(
        "--quick",
        action="store_true",
        help="Executa apenas testes rápidos (pula integração)"
    )
    
    parser.add_argument(
        "--verbose",
        action="store_true",
        help="Modo verboso com mais detalhes"
    )
    
    args = parser.parse_args()
    
    # Configurar modo
    skip_integration = args.skip_integration or args.quick
    continue_on_error = args.continue_on_error
    
    print_header("🚀 EAÍ GATEWAY - SUITE DE TESTES")
    print(f"📅 Executado em: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print(f"⚙️ Configuração:")
    print(f"   • Pular integração: {skip_integration}")
    print(f"   • Continuar com erro: {continue_on_error}")
    print(f"   • Modo rápido: {args.quick}")
    print(f"   • Modo verboso: {args.verbose}")
    
    results = []
    total_start_time = time.time()
    
    try:
        # 1. Testes de Message Formatter (sempre executar)
        success, duration = run_test_suite(
            "Message Formatter Tests", 
            run_message_formatter_tests,
            skip_on_error=continue_on_error
        )
        results.append(("Message Formatter", success, duration))
        
        # 2. Testes de Event Loop (sempre executar)
        success, duration = run_test_suite(
            "Event Loop Fixes Tests",
            run_event_loop_tests,
            skip_on_error=continue_on_error  
        )
        results.append(("Event Loop Fixes", success, duration))
        
        # 3. Testes de Integração (condicional)
        if not skip_integration:
            success, duration = run_test_suite(
                "Provider Integration Tests",
                run_provider_integration_tests,
                skip_on_error=True  # Sempre continuar para integração
            )
            results.append(("Provider Integration", success, duration))
        else:
            print_header("⏭️ PULANDO: Provider Integration Tests")
            print("   Motivo: --skip-integration ou --quick especificado")
            
    except KeyboardInterrupt:
        print("\n\n🛑 EXECUÇÃO INTERROMPIDA PELO USUÁRIO")
        return 1
        
    except Exception as e:
        print(f"\n\n💥 ERRO FATAL NA EXECUÇÃO: {e}")
        if args.verbose:
            import traceback
            traceback.print_exc()
        return 1
    
    # Calcular tempo total
    total_duration = time.time() - total_start_time
    
    # Imprimir resumo
    print_summary(results)
    
    # Determinar código de saída
    all_passed = all(passed for _, passed, _ in results)
    
    if all_passed:
        print("\n🎉 SUCESSO: Todos os testes passaram!")
        return 0
    else:
        failed_tests = [name for name, passed, _ in results if not passed]
        print(f"\n💥 FALHA: Testes falharam em: {', '.join(failed_tests)}")
        return 1


if __name__ == "__main__":
    exit_code = main()
    sys.exit(exit_code)