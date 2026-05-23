# Meta-direct cutover — checklist operacional (PR #32 + companion #22)

Sequência exata pra ligar `META_DIRECT_ENABLED=true` em staging/prod sem
quebrar o tráfego atual. Companion: ADR-030 no repo `study-sf-whatsapp-poc1`.

## Pré-requisitos (antes de qualquer merge)

### Gateway K8s secret `eai-agent-gateway-secrets`

```bash
# Operador adiciona as 8 vars novas no secret K8s (Infisical → sync K8s).
# Sem isso o pod sobe, mas features novas ficam dormentes.
kubectl edit secret eai-agent-gateway-secrets -n <namespace>
```

Adicionar (base64-encoded conforme padrão K8s secret):

| Variável | Origem | Quando obrigatória |
|---|---|---|
| `META_WEBHOOK_VERIFY_TOKEN` | Meta App config (qualquer string forte) | Antes do flip `META_DIRECT_ENABLED=true` |
| `META_WEBHOOK_APP_SECRET` | Meta App → Settings → App Secret | Antes do flip |
| `META_SYSTEM_USER_TOKEN` | Meta Business Manager → System User → Bearer token | Antes do flip |
| `META_PHONE_NUMBER_ID` | Meta WABA → Phone Numbers (não-secreto) | Antes do flip |
| `META_SELF_CALLBACK_URL` | URL pública do próprio Gateway | Antes do flip |
| `META_DISPATCH_SECRET` | Gerar string forte 32+ chars | Antes do flip |
| `ADMIN_API_TOKEN` | Gerar string forte 32+ chars | Antes de mergear PR #22 |
| `META_FLOW_REGISTRY` (opcional) | Formato `luminaria:reparo_luminaria` | Pra Flow Luminária |

**Default seguro**: pod sobe sem nenhuma dessas. `META_DIRECT_ENABLED=false`
default. Endpoint `/admin/broker-mode` responde 503 até `ADMIN_API_TOKEN`
populado — Mule trata como degraded e cai pro fallback estático.

### Mule Runtime Manager Properties (após merge PR #22 + redeploy Mule)

```
broker.modeHost = services.staging.app.dados.rio   # ou services.pref.rio em prod
broker.modePath = /eai-agent-gateway/admin/broker-mode
broker.modeAuthToken = <mesmo valor de ADMIN_API_TOKEN do Gateway>
broker.salesforceBrokerEnabledFallback = true       # default; preserva SF ON em outage Gateway
```

### Salesforce (Anonymous Apex pós-deploy)

```apex
// 1. Popular Custom Setting:
Broker_Mode_Config__c cfg = Broker_Mode_Config__c.getOrgDefaults();
if (cfg == null) cfg = new Broker_Mode_Config__c();
cfg.SetupOwnerId = UserInfo.getOrganizationId();
cfg.Endpoint_URL__c = 'https://services.staging.app.dados.rio/eai-agent-gateway/admin/broker-mode';
cfg.Admin_API_Token__c = '<mesmo valor de ADMIN_API_TOKEN>';
upsert cfg;

// 2. Assign Permission Set ao Automated Process user (CRÍTICO):
PermissionSet ps = [SELECT Id FROM PermissionSet WHERE Name = 'Chatbot_Pipeline_Access' LIMIT 1];
User autoUser = [SELECT Id FROM User WHERE Alias = 'autoproc' LIMIT 1];
insert new PermissionSetAssignment(AssigneeId = autoUser.Id, PermissionSetId = ps.Id);

// 3. Confirmar:
System.debug([SELECT Assignee.Name FROM PermissionSetAssignment
              WHERE PermissionSet.Name = 'Chatbot_Pipeline_Access']);
```

## Sequência de merge

### Passo 1 — Merge PR #32 (Gateway)
- Endpoint `/admin/broker-mode` fica disponível pra Mule pollar.
- `META_DIRECT_ENABLED` continua `false` default → rotas `/meta/*` NÃO
  registradas. Comportamento idêntico ao staging atual.
- Smoke pós-deploy (5min):
  ```bash
  # 1. Pod boot sem warn de chain incompleta:
  kubectl logs -l app=gateway -n <ns> | grep meta_direct_chain_incomplete

  # 2. Health:
  curl -fsS https://<gateway-host>/health

  # 3. Admin endpoint (com token):
  curl -fsS -H "X-Admin-Token: $ADMIN_API_TOKEN" \
    https://<gateway-host>/admin/broker-mode
  # Expected: {"salesforce_broker_enabled":true,"updated_at":"..."}

  # 4. Path legacy continua funcionando:
  curl -fsS -X POST https://<gateway-host>/api/v1/message/webhook/user \
    -H 'Content-Type: application/json' \
    -d '{"user_number":"+5521xxxxxxxxx","message":"smoke","provider":"google_agent_engine"}'
  ```

### Passo 2 — Merge PR #22 (study-sf — Mule + Apex)
- Redeploy Mule via `bash components/mulesoft/scripts/deploy-cloudhub.sh`.
- Deploy Apex via `sf project deploy start --target-org qa`.
- Smoke pós-deploy (10min):
  ```bash
  # 1. CloudHub log Mule: poll a cada 30s funcionando:
  # Esperado: event=broker_mode_polled salesforce_broker_enabled=true

  # 2. Apex async log:
  # Esperado: BrokerGatedPollQueueable rodando + runPollWork sem skip
  # (assume broker=true pelo fail-safe)
  ```

### Passo 3 — Configurar Custom Setting + PS no SF (opcional, opt-in)
- Sem essa config, Apex assume broker ON (fail-safe) e auto-suspend nunca dispara.
- Com a config: quando `SALESFORCE_BROKER_ENABLED=false` for setado,
  `ConversationPollSchedulable` pula o trabalho real e zera quota SF.

## Flip operacional (depois dos 3 passos)

### Ligar Meta-direct mode (manter Salesforce broker ON)

```bash
# Infisical → eai-agent-gateway/staging:
META_DIRECT_ENABLED=true
# (+ todas as outras META_* já populadas)
```

Pod reinicia. Logs esperados:
- `event=meta_direct_enabled` ✓
- Sem `event=meta_direct_chain_incomplete` ✓

Apontar Meta App Callback URL pro Gateway:
- Meta App Dashboard → WhatsApp → Configuration → Webhook URL =
  `https://<gateway-host>/meta/webhook`
- Verify Token = `META_WEBHOOK_VERIFY_TOKEN`
- Subscribe to `messages` field.

Após verify handshake OK, Gateway recebe inbound direto.

### Desligar Salesforce broker (100% Meta-direto)

```bash
# Infisical:
SALESFORCE_BROKER_ENABLED=false
```

Pod reinicia. Em <60s:
- Mule poll pega novo valor → Object Store atualiza
- `/sc/inbound` no Mule passa a rejeitar 503
- Callback Mule força Meta Graph (skip SCRT)
- Handoff pula Case creation
- Apex (se Custom Setting populado) auto-suspende `runPollWork`

### Reverter

```bash
# Volta a Salesforce broker (idempotente):
SALESFORCE_BROKER_ENABLED=true
```

Propaga em <60s pelos mesmos paths.

### Reverter completo (desliga Meta-direct)

```bash
META_DIRECT_ENABLED=false
```

Pod reinicia. Rotas `/meta/*` deixam de ser registradas. Operador
reaponta Meta App Callback URL pro Mule manualmente (operação no Meta
App Dashboard, não tem API).

## Rollback emergencial dos PRs

```bash
# Em caso de regressão grave:
git revert <merge-sha-pr-32>  # Gateway
git revert <merge-sha-pr-22>  # study-sf
# Push em staging, redeploy.
```

Custom Setting + Remote Sites do SF são additions seguras — operador apaga
via Setup UI se quiser limpar.
