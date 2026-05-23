// Package handlers — WhatsApp Flow registry (flow_name → service_name).
//
// Mirror Mule `whatsapp.flow.registry` (ADR-024). Quando cidadão submete
// um WhatsApp Flow (`interactive.nfm_reply`), MetaWebhookHandler:
//
//  1. Extrai flow_name + response_json do payload
//  2. Resolve service_name via FlowRegistry (substring-tolerante,
//     case-insensitive). Default fallback se nenhum bater.
//  3. Inclui `service_name` + `form_submission` (estruturado, parseado
//     do response_json) em `metadata` pro Engine rotear pra handler correto.
//
// Adicionar Flow novo = adicionar entry no registry via env var
// (META_FLOW_REGISTRY="flow1:service1;flow2:service2"). Sem redeploy.
package handlers

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// stripDiacritics remove acentos pra match insensitive a diacritico.
// "Luminária Quebrada" → "Luminaria Quebrada". Falha de transform retorna
// input cru (best-effort).
func stripDiacritics(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	res, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	return res
}

// normalizeForMatch — lowercase + strip diacritics. Usado em ambos os lados
// (entry no registry e flow_name do Meta) pra match robusto.
func normalizeForMatch(s string) string {
	return stripDiacritics(strings.ToLower(strings.TrimSpace(s)))
}

// FlowRegistry resolve flow_name → service_name. Imutável após NewFlowRegistry.
type FlowRegistry struct {
	entries        []flowEntry
	defaultService string
}

type flowEntry struct {
	flowNameNormalized string // lowercase + sem diacritico
	serviceName        string
}

// NewFlowRegistry parseia a config-string e constrói o lookup. Formato:
// "flow_name_1:service_1;flow_name_2:service_2" (semicolon-separated,
// colon-separated). Whitespace é trimmed. Entries malformadas (sem `:`)
// são ignoradas silenciosamente.
func NewFlowRegistry(config, defaultService string) *FlowRegistry {
	r := &FlowRegistry{defaultService: defaultService}
	for _, part := range strings.Split(config, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		colon := strings.IndexByte(part, ':')
		if colon <= 0 || colon == len(part)-1 {
			continue
		}
		flow := strings.TrimSpace(part[:colon])
		service := strings.TrimSpace(part[colon+1:])
		if flow == "" || service == "" {
			continue
		}
		r.entries = append(r.entries, flowEntry{
			flowNameNormalized: normalizeForMatch(flow),
			serviceName:        service,
		})
	}
	// Ordena longest-first pra resolver match por especificidade, não por
	// ordem de declaração. Sem isso, registry "luz:eletrica;luminaria:reparo"
	// com flow_name "Luminária Quebrada" casaria "luz" primeiro (substring),
	// retornando "eletrica" em vez de "reparo". Risco operacional invisível
	// pro operador setando o registry. Longest-first elimina a armadilha:
	// entries mais específicas (mais longas) sempre ganham.
	sort.SliceStable(r.entries, func(i, j int) bool {
		return len(r.entries[i].flowNameNormalized) > len(r.entries[j].flowNameNormalized)
	})
	return r
}

// Resolve retorna o service_name pra um flow_name. Match em ordem:
//  1. substring case-insensitive + diacritic-insensitive, ENTRIES ORDENADAS
//     LONGEST-FIRST. Entry "luminaria" bate flow_name "Luminária Quebrada".
//     Longest-first garante que registry "luz:X;luminaria:Y" resolve
//     "Luminária Quebrada" pra "Y" (não pra "X") — match por especificidade.
//  2. defaultService se nada bater.
//  3. "" se default também vazio.
func (r *FlowRegistry) Resolve(flowName string) string {
	if r == nil {
		return ""
	}
	target := normalizeForMatch(flowName)
	if target != "" {
		for _, e := range r.entries {
			if strings.Contains(target, e.flowNameNormalized) {
				return e.serviceName
			}
		}
	}
	return r.defaultService
}

// ParseFlowResponse parse o `response_json` (string JSON enviada pela
// Meta no nfm_reply.response_json) em map estruturado. Retorna nil em
// erros de parsing — caller cai pra propagar o raw.
func ParseFlowResponse(responseJSON string) map[string]interface{} {
	if responseJSON == "" {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(responseJSON), &m); err != nil {
		return nil
	}
	return m
}
