package pii

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxErrorSummaryRunes limita o resumo de erro gravado em log ou span: um erro
// que ainda carregue texto de terceiros não vira um dump inteiro.
const maxErrorSummaryRunes = 300

const (
	omittedPath    = "/[omitido]"
	truncatedTail  = "…[truncado]"
	unknownURLHost = "unknown"
)

var (
	// urlPattern casa URLs absolutas dentro de um texto de erro. O erro de
	// transporte do Go embute a URL entre aspas (`Get "https://...": ...`), por
	// isso a URL termina em espaço, aspas ou sinais de maior/menor.
	urlPattern = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s"'<>]+`)

	// remotePayloadPatterns marcam onde começa um conteúdo devolvido pelo
	// serviço remoto (corpo da resposta HTTP ou JSON de erro da operação). O
	// grupo 1 é o que fica; tudo a partir do fim dele é descartado. Os erros
	// são embrulhados com `fmt.Errorf("...: %w", err)`, então o corpo é sempre
	// o fim do texto.
	remotePayloadPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?s)(non-2xx response: \d{3}) - .*$`),
		regexp.MustCompile(`(?s)(returned status \d{3}): .*$`),
		regexp.MustCompile(`(?s)(operation finished with error):? .*$`),
		regexp.MustCompile(`(?s)() \(raw: .*$`),
	}

	// googleStatusPattern extrai o enum de status do erro do Google
	// (`"status": "FAILED_PRECONDITION"`), que classifica o erro sem carregar
	// texto livre.
	googleStatusPattern = regexp.MustCompile(`"status"\s*:\s*"([A-Z][A-Z_]{2,40})"`)
)

// ErrorSummary devolve um texto curto do erro, seguro para log e atributo de
// span: sem o corpo de respostas remotas (que pode repetir a conversa do
// cidadão), com toda URL reduzida ao esquema e host, e limitado em tamanho.
// Mantém o status HTTP e, quando o corpo é um erro do Google, o enum de status.
//
// Use-o no lugar de err.Error() em telemetria e log. O erro original continua
// valendo para a lógica (classificação de retry, mensagens ao usuário).
func ErrorSummary(err error) string {
	if err == nil {
		return ""
	}

	summary := err.Error()
	for _, pattern := range remotePayloadPatterns {
		loc := pattern.FindStringSubmatchIndex(summary)
		if loc == nil {
			continue
		}
		removed := summary[loc[3]:]
		summary = summary[:loc[3]]
		if status := googleStatusPattern.FindStringSubmatch(removed); status != nil {
			summary += " (" + status[1] + ")"
		}
	}

	summary = RedactURLs(summary)
	return truncateRunes(summary, maxErrorSummaryRunes)
}

// RedactURLs troca cada URL do texto pelo esquema e host, sem caminho, query,
// fragmento nem credenciais: a URL de mídia identifica o arquivo do cidadão e
// a query costuma carregar assinatura de acesso.
func RedactURLs(text string) string {
	return urlPattern.ReplaceAllStringFunc(text, func(raw string) string {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			return schemeOf(raw) + "://" + unknownURLHost + omittedPath
		}
		redacted := parsed.Scheme + "://" + parsed.Host
		if strings.Trim(parsed.EscapedPath(), "/") != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			redacted += omittedPath
		}
		return redacted
	})
}

// URLHost devolve só o host de uma URL (sem credenciais, caminho nem query),
// ou "unknown" quando o texto não é uma URL absoluta.
func URLHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return unknownURLHost
	}
	return parsed.Host
}

// schemeOf devolve o esquema de uma URL que url.Parse recusou.
func schemeOf(raw string) string {
	if i := strings.Index(raw, "://"); i > 0 {
		return strings.ToLower(raw[:i])
	}
	return "url"
}

func truncateRunes(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + truncatedTail
}
