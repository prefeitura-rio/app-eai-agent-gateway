# Testes do EAí Gateway

Este diretório contém testes abrangentes para verificar se o sistema está funcionando corretamente.

## 📋 Estrutura dos Testes

### 🧪 Suítes de Teste

1. **Message Formatter Tests** (`test_message_formatter.py`)
   - Testa formatação de mensagens para WhatsApp
   - Verifica conversão Markdown → WhatsApp  
   - Testa compatibilidade com diferentes formatos (Google, Letta, LangChain)
   - Verifica preservação de conteúdo

2. **Event Loop Fixes Tests** (`test_event_loop_fixes.py`)
   - Testa correções para "Event loop is closed"
   - Verifica funcionamento em diferentes cenários
   - Testa concorrência e performance
   - Verifica propagação de exceções

3. **Provider Integration Tests** (`test_providers_integration.py`)
   - Testa integração com Letta service
   - Testa integração com Google Agent Engine
   - Verifica Provider Factory
   - Testa tratamento de erros

## 🚀 Como Executar

### Execução Simples

```bash
# Executar todos os testes
just test

# Ou manualmente:
uv run python tests/run_all_tests.py
```

### Opções Avançadas

```bash
# Executar apenas testes rápidos (sem integração)
uv run python tests/run_all_tests.py --quick

# Pular testes de integração
uv run python tests/run_all_tests.py --skip-integration

# Continuar mesmo com erros
uv run python tests/run_all_tests.py --continue-on-error

# Modo verboso
uv run python tests/run_all_tests.py --verbose

# Ajuda
uv run python tests/run_all_tests.py --help
```

### Execução Individual

```bash
# Testar apenas formatação de mensagens
uv run python tests/test_message_formatter.py

# Testar apenas event loop fixes
uv run python tests/test_event_loop_fixes.py

# Testar apenas integração
uv run python tests/test_providers_integration.py
```

## 🎯 Quando Executar

### Durante Desenvolvimento
- Execute `just test --quick` antes de fazer commits
- Execute testes específicos quando modificar componentes relacionados

### Verificação Completa
- Execute `just test` após mudanças significativas
- Execute antes de fazer deploy para produção
- Execute periodicamente para verificar saúde do sistema

### Monitoramento
- Execute testes de integração para verificar conectividade com APIs externas
- Use em scripts de monitoramento/health check

## 📊 Interpretando Resultados

### ✅ Sucesso
```
🎉 TODOS OS TESTES PASSARAM!
   O sistema está funcionando corretamente.
```

### ❌ Falha
```
💥 ALGUNS TESTES FALHARAM!
   Verifique os logs acima para detalhes dos problemas.
```

### ⚠️ Falhas Esperadas
- **Testes de Integração**: Podem falhar se APIs externas estiverem indisponíveis
- **Testes de Network**: Podem falhar em ambientes sem internet
- **Testes de Auth**: Podem falhar se credenciais não estiverem configuradas

## 🔧 Solução de Problemas

### Erros Comuns

1. **Import Errors**
   ```bash
   # Verificar se está no diretório correto
   cd /path/to/app-eai-agent-gateway
   
   # Verificar se dependências estão instaladas
   uv sync
   ```

2. **API Connection Errors**
   ```bash
   # Verificar variáveis de ambiente
   cat .env
   
   # Verificar conectividade
   just health
   ```

3. **Redis Errors**
   ```bash
   # Verificar se Redis está rodando
   just test-services
   ```

### Ambiente de Desenvolvimento

Para ambiente de desenvolvimento, você pode:

```bash
# Rodar apenas testes que não dependem de APIs externas
uv run python tests/run_all_tests.py --skip-integration

# Ou usar modo rápido
just test --quick
```

## 📈 Métricas

Os testes fornecem métricas sobre:
- ⏱️ **Tempo de execução** - Performance dos componentes
- 🎯 **Taxa de sucesso** - Confiabilidade do sistema  
- 🔧 **Coverage** - Quais componentes estão sendo testados
- 📊 **Tendências** - Compare execuções ao longo do tempo

## 🛡️ Segurança

Os testes são projetados para:
- ✅ **Não expor dados sensíveis** nos logs
- ✅ **Usar dados de teste** em vez de dados reais
- ✅ **Limpar dados** criados durante os testes
- ✅ **Respeitar rate limits** das APIs externas

## 🤝 Contribuindo

Ao adicionar novos recursos:

1. **Adicione testes** para novas funcionalidades
2. **Atualize testes existentes** se necessário
3. **Execute todos os testes** antes de fazer PR
4. **Documente casos especiais** nos comentários do código

### Template para Novos Testes

```python
def test_nova_funcionalidade():
    """Testa nova funcionalidade X"""
    print("🧪 Testando nova funcionalidade...")
    
    try:
        # Arrange
        # ...
        
        # Act
        # ...
        
        # Assert
        assert resultado_esperado, "Mensagem de erro clara"
        
        print("✅ Nova funcionalidade OK")
        return True
        
    except Exception as e:
        print(f"❌ Erro na nova funcionalidade: {e}")
        traceback.print_exc()
        return False
```