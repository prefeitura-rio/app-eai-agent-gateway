// Package handlers — constantes de dedup compartilhadas entre
// meta_webhook.go (inbound, key `meta:wamid:<wamid>`) e meta_dispatch.go
// (outbound, key `meta:dispatch:sent:<message_id>`).
//
// Os dois keyspaces são distintos mas usam o mesmo conjunto de sentinelas
// pra value das chaves — agrupados aqui pra evitar cross-file coupling
// implícito (constante definida num arquivo e usada noutro sem doc).
//
// TTL nominal de 24h em ambos: bate com o máximo de uma session window
// do WhatsApp Business; suficiente pra absorver Meta retry aggressive
// (primeiros minutos pós-envio) sem reter histórico indefinido.
package handlers

import "time"

// dedupTTL — TTL de claim em ambos os keyspaces.
//
// Inbound (`meta:wamid:<wamid>`): claim marca wamid como "já processado"
// pra dedup de Meta retries.
// Outbound (`meta:dispatch:sent:<message_id>`): claim marca message_id
// como "já dispatchado" pra dedup de worker retries pós-callback.
const dedupTTL = 24 * time.Hour

// dispatchSentMarkerTTL — alias semântico de dedupTTL no contexto outbound.
// Mantido separado pro caso de divergir no futuro (ex: outbound poder ter
// TTL mais curto se idempotência de wamid via Meta API for confiável).
const dispatchSentMarkerTTL = 24 * time.Hour

// Sentinelas pra value das claims. Definição centralizada porque
// `dedupValueNoContent` é escrito por meta_dispatch.go mas lido nos testes
// e referenciado nos comentários de meta_webhook.go — declarar onde é
// escrito (dispatch) deixava o read-site sem visibilidade do conjunto
// completo de estados possíveis.
const (
	// dedupValueInflight — claim ativa, processamento em curso. Concurrent
	// retries veem este valor e recebem 503 (Meta retenta).
	dedupValueInflight = "inflight"
	// dedupValueDone — inbound completou enqueue. Duplicatas viram 200.
	dedupValueDone = "done"
	// dedupValueNoContent — outbound decidiu não enviar (callback sem
	// texto nem media envelope). Sentinela terminal; concurrent retries
	// veem "already_sent" em vez de 503, evitando ficar presos 24h.
	dedupValueNoContent = "no_content"
)
