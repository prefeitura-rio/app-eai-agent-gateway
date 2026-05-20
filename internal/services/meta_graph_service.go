// Package services — Meta Graph API client for outbound WhatsApp Business
// messages. POC scope (`feat/meta-direct-poc`): text-only send.
//
// Equivalente ao Mule `send-via-meta-graph-flow` em `webhook-flow.xml`. Quando
// Gateway absorver completamente o Mule, este client cobrirá também media
// upload + send (image/audio/video/document/sticker), location, template,
// interactive — fase posterior do migration plan.
//
// Endpoint Meta:
//
//	POST https://graph.facebook.com/{api_version}/{phone_number_id}/messages
//	Authorization: Bearer {system_user_token}
//	Content-Type: application/json
//
// Retries: o caller (worker) já tem retry/backoff; este client só faz 1
// tentativa por chamada. Erros 5xx do Meta retornam erro pro caller decidir.
package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/prefeitura-rio/app-eai-agent-gateway/internal/config"
)

// DecodeBase64 é helper público pra callers decodificarem o base64 do
// envelope antes de chamar UploadMedia. Centraliza pra evitar imports
// extras em packages handlers.
func DecodeBase64(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("base64 string empty")
	}
	return base64.StdEncoding.DecodeString(s)
}

// MetaGraphService — client HTTP para Meta WhatsApp Business Cloud API.
type MetaGraphService struct {
	cfg        *config.MetaConfig
	httpClient *http.Client
	logger     *logrus.Logger
}

// NewMetaGraphService constrói o client. Timeout 15s alinhado com Mule
// `send-via-meta-graph-flow` (config: 15s connect + 15s response).
func NewMetaGraphService(cfg *config.MetaConfig, logger *logrus.Logger) *MetaGraphService {
	return &MetaGraphService{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		logger: logger,
	}
}

// metaSendTextRequest — payload mínimo Meta /messages pra texto.
type metaSendTextRequest struct {
	MessagingProduct string          `json:"messaging_product"`
	RecipientType    string          `json:"recipient_type"`
	To               string          `json:"to"`
	Type             string          `json:"type"`
	Text             metaTextPayload `json:"text"`
}

type metaTextPayload struct {
	PreviewURL bool   `json:"preview_url"`
	Body       string `json:"body"`
}

// MetaSendResponse — shape do success response do Meta /messages.
type MetaSendResponse struct {
	MessagingProduct string                  `json:"messaging_product"`
	Contacts         []MetaSendContactRef    `json:"contacts"`
	Messages         []MetaSendMessageResult `json:"messages"`
}

// MetaSendContactRef — eco do destinatário (Meta normaliza).
type MetaSendContactRef struct {
	Input string `json:"input"`
	WAID  string `json:"wa_id"`
}

// MetaSendMessageResult — id retornado por mensagem enviada (wamid).
type MetaSendMessageResult struct {
	ID string `json:"id"`
}

// SendText envia mensagem texto pro Meta Graph API.
//
// Retorna wamid em sucesso ou erro descritivo. Não faz retry — caller decide.
// PII (texto) NUNCA aparece em log; só nome + len + status code.
func (s *MetaGraphService) SendText(ctx context.Context, recipient, body string) (string, error) {
	if !s.cfg.Enabled {
		return "", fmt.Errorf("meta direct integration disabled (META_DIRECT_ENABLED=false)")
	}
	if s.cfg.SystemUserToken == "" || s.cfg.PhoneNumberID == "" {
		return "", fmt.Errorf("meta credentials missing (SystemUserToken or PhoneNumberID empty)")
	}
	if recipient == "" || body == "" {
		return "", fmt.Errorf("recipient and body must be non-empty")
	}

	reqBody := metaSendTextRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               recipient,
		Type:             "text",
		Text:             metaTextPayload{PreviewURL: false, Body: body},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	url := fmt.Sprintf(
		"https://graph.facebook.com/%s/%s/messages",
		s.cfg.GraphAPIVersion, s.cfg.PhoneNumberID,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SystemUserToken)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.logger.WithFields(logrus.Fields{
			"event":    "meta_graph_send_error",
			"recipient_prefix": recipientPrefix(recipient),
			"duration_ms":      time.Since(start).Milliseconds(),
			"error":            err.Error(),
		}).Error("Meta Graph send failed")
		return "", fmt.Errorf("http send: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	logFields := logrus.Fields{
		"event":            "meta_graph_send",
		"recipient_prefix": recipientPrefix(recipient),
		"status_code":      resp.StatusCode,
		"duration_ms":      time.Since(start).Milliseconds(),
		"body_length":      len(body),
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Snippet do erro pra debug — Meta retorna error.message útil mas
		// pode conter PII; truncar pra primeiros 200 chars.
		errSnippet := string(respBody)
		if len(errSnippet) > 200 {
			errSnippet = errSnippet[:200] + "...(truncated)"
		}
		logFields["error_body"] = errSnippet
		s.logger.WithFields(logFields).Error("Meta Graph send non-2xx")
		return "", fmt.Errorf("meta returned status %d: %s", resp.StatusCode, errSnippet)
	}

	var sendResp MetaSendResponse
	if err := json.Unmarshal(respBody, &sendResp); err != nil {
		s.logger.WithFields(logFields).WithError(err).Error("Meta Graph response parse failed")
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(sendResp.Messages) == 0 || sendResp.Messages[0].ID == "" {
		s.logger.WithFields(logFields).Warn("Meta Graph success response without wamid")
		return "", fmt.Errorf("no wamid in response")
	}

	wamid := sendResp.Messages[0].ID
	logFields["wamid"] = wamid
	s.logger.WithFields(logFields).Info("Meta Graph send success")
	return wamid, nil
}

// recipientPrefix mascara telefone pra log (LGPD/PII).
// "5521965850470" → "55219658…" (8 chars + suffix).
func recipientPrefix(phone string) string {
	if len(phone) <= 8 {
		return phone
	}
	return phone[:8] + "…"
}

// ─── Media outbound ──────────────────────────────────────────────────────
//
// Pattern Meta /messages pra media é:
//
//	{
//	  "messaging_product": "whatsapp",
//	  "to": "<phone>",
//	  "type": "image|audio|video|document|sticker",
//	  "<type>": { "id": "<media_id>" }                  // upload via /media
//	         OR { "link": "https://..." }                // URL pública
//	         + opcional "caption", "filename" (document)
//	}
//
// `id` (via upload Meta /media) é preferível: payload menor, Meta dedupa,
// pode reusar entre destinatários. `link` mais simples mas exige URL HTTPS
// publicamente acessível (não vai funcionar via Cloud Run interno).
//
// `SendMedia` aceita ambos: passar `MediaInput.ID` OU `MediaInput.Link`.
// Se ambos preenchidos, ID ganha precedência (Meta-side requirement).

// MediaInput descreve a media a ser enviada. Use exatamente um de {ID, Link}.
type MediaInput struct {
	// ID é o media handle retornado por upload pra Meta /media (preferível).
	ID string
	// Link é uma URL HTTPS pública pro conteúdo (alternativa simples).
	Link string
	// Caption: opcional pra image/video/document; ignorado por audio/sticker.
	Caption string
	// Filename: opcional pra document.
	Filename string
	// Voice: opcional pra audio (true = voice note no WhatsApp).
	Voice bool
}

type metaMediaSendRequest struct {
	MessagingProduct string                 `json:"messaging_product"`
	RecipientType    string                 `json:"recipient_type"`
	To               string                 `json:"to"`
	Type             string                 `json:"type"`
	Image            *metaMediaPayload      `json:"image,omitempty"`
	Audio            *metaMediaPayload      `json:"audio,omitempty"`
	Video            *metaMediaPayload      `json:"video,omitempty"`
	Document         *metaMediaPayload      `json:"document,omitempty"`
	Sticker          *metaMediaPayload      `json:"sticker,omitempty"`
	Location         *metaLocationPayload   `json:"location,omitempty"`
	Template         *metaTemplatePayload   `json:"template,omitempty"`
	Interactive      *metaInteractiveSend   `json:"interactive,omitempty"`
	Reaction         *metaReactionPayload   `json:"reaction,omitempty"`
	Context          *metaContextPayload    `json:"context,omitempty"`
}

type metaMediaPayload struct {
	ID       string `json:"id,omitempty"`
	Link     string `json:"link,omitempty"`
	Caption  string `json:"caption,omitempty"`
	Filename string `json:"filename,omitempty"`
	Voice    bool   `json:"voice,omitempty"`
}

type metaLocationPayload struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name,omitempty"`
	Address   string  `json:"address,omitempty"`
}

type metaTemplatePayload struct {
	Name       string                       `json:"name"`
	Language   metaTemplateLanguage         `json:"language"`
	Components []map[string]interface{}     `json:"components,omitempty"`
}

type metaTemplateLanguage struct {
	Code string `json:"code"`
}

type metaInteractiveSend struct {
	Type   string                 `json:"type"`
	Header map[string]interface{} `json:"header,omitempty"`
	Body   map[string]interface{} `json:"body,omitempty"`
	Footer map[string]interface{} `json:"footer,omitempty"`
	Action map[string]interface{} `json:"action"`
}

type metaReactionPayload struct {
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
}

type metaContextPayload struct {
	MessageID string `json:"message_id"`
}

func (s *MetaGraphService) credentialsReady() error {
	if !s.cfg.Enabled {
		return fmt.Errorf("meta direct integration disabled (META_DIRECT_ENABLED=false)")
	}
	if s.cfg.SystemUserToken == "" || s.cfg.PhoneNumberID == "" {
		return fmt.Errorf("meta credentials missing (SystemUserToken or PhoneNumberID empty)")
	}
	return nil
}

func mediaPayloadFromInput(in MediaInput) (*metaMediaPayload, error) {
	if in.ID == "" && in.Link == "" {
		return nil, fmt.Errorf("media requires either id or link")
	}
	p := &metaMediaPayload{
		Caption:  in.Caption,
		Filename: in.Filename,
		Voice:    in.Voice,
	}
	if in.ID != "" {
		p.ID = in.ID
	} else {
		p.Link = in.Link
	}
	return p, nil
}

// SendMedia envia image/audio/video/document/sticker pro Meta.
// `mediaType` deve ser um dos: "image","audio","video","document","sticker".
func (s *MetaGraphService) SendMedia(ctx context.Context, recipient, mediaType string, in MediaInput) (string, error) {
	if err := s.credentialsReady(); err != nil {
		return "", err
	}
	if recipient == "" {
		return "", fmt.Errorf("recipient must be non-empty")
	}
	allowed := map[string]bool{"image": true, "audio": true, "video": true, "document": true, "sticker": true}
	if !allowed[mediaType] {
		return "", fmt.Errorf("invalid mediaType %q", mediaType)
	}
	payload, err := mediaPayloadFromInput(in)
	if err != nil {
		return "", err
	}
	// Caption só vale pra image/video/document.
	if mediaType == "audio" || mediaType == "sticker" {
		payload.Caption = ""
		payload.Filename = ""
	}
	// Voice só vale pra audio.
	if mediaType != "audio" {
		payload.Voice = false
	}
	req := metaMediaSendRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               recipient,
		Type:             mediaType,
	}
	switch mediaType {
	case "image":
		req.Image = payload
	case "audio":
		req.Audio = payload
	case "video":
		req.Video = payload
	case "document":
		req.Document = payload
	case "sticker":
		req.Sticker = payload
	}
	return s.doSend(ctx, recipient, mediaType, req)
}

// SendLocation envia coordenadas (anexo "Localização" no WhatsApp).
func (s *MetaGraphService) SendLocation(ctx context.Context, recipient string, lat, lng float64, name, address string) (string, error) {
	if err := s.credentialsReady(); err != nil {
		return "", err
	}
	if recipient == "" {
		return "", fmt.Errorf("recipient must be non-empty")
	}
	req := metaMediaSendRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               recipient,
		Type:             "location",
		Location: &metaLocationPayload{
			Latitude: lat, Longitude: lng, Name: name, Address: address,
		},
	}
	return s.doSend(ctx, recipient, "location", req)
}

// SendTemplate envia template aprovado pelo Meta (uso pra mensagens proativas
// fora da janela 24h ou pra HSM transactional). `components` deve seguir
// o shape Meta — typicamente:
//
//	[
//	  { "type":"header", "parameters":[ ... ] },
//	  { "type":"body",   "parameters":[ {"type":"text","text":"..."} ] }
//	]
func (s *MetaGraphService) SendTemplate(ctx context.Context, recipient, name, langCode string, components []map[string]interface{}) (string, error) {
	if err := s.credentialsReady(); err != nil {
		return "", err
	}
	if recipient == "" || name == "" || langCode == "" {
		return "", fmt.Errorf("recipient, template name, and language code required")
	}
	req := metaMediaSendRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               recipient,
		Type:             "template",
		Template: &metaTemplatePayload{
			Name:       name,
			Language:   metaTemplateLanguage{Code: langCode},
			Components: components,
		},
	}
	return s.doSend(ctx, recipient, "template", req)
}

// SendInteractive envia button/list/flow message. `action` é obrigatório pelo
// Meta — formato depende do subtype:
//
//	button:        action.buttons = [{ type:"reply", reply:{id, title}}, ...]
//	list:          action.button = "..."; action.sections = [...]
//	product:       action.catalog_id, action.product_retailer_id
//	flow:          action.parameters = { flow_token, flow_id, flow_cta, ... }
//
// `subtype` valida apenas valores conhecidos do Meta API; demais campos
// passam-through como caller passou (Engine monta o shape correto).
func (s *MetaGraphService) SendInteractive(ctx context.Context, recipient, subtype string, header, body, footer, action map[string]interface{}) (string, error) {
	if err := s.credentialsReady(); err != nil {
		return "", err
	}
	if recipient == "" || subtype == "" {
		return "", fmt.Errorf("recipient and subtype required")
	}
	if action == nil {
		return "", fmt.Errorf("interactive.action is required by Meta")
	}
	allowed := map[string]bool{"button": true, "list": true, "product": true, "product_list": true, "flow": true}
	if !allowed[subtype] {
		return "", fmt.Errorf("invalid interactive subtype %q", subtype)
	}
	req := metaMediaSendRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               recipient,
		Type:             "interactive",
		Interactive: &metaInteractiveSend{
			Type:   subtype,
			Header: header,
			Body:   body,
			Footer: footer,
			Action: action,
		},
	}
	return s.doSend(ctx, recipient, "interactive_"+subtype, req)
}

// SendReaction envia emoji reaction a mensagem prévia. `wamid` é o ID da
// mensagem alvo; `emoji` é o caractere unicode (string vazia remove a reaction).
func (s *MetaGraphService) SendReaction(ctx context.Context, recipient, wamid, emoji string) (string, error) {
	if err := s.credentialsReady(); err != nil {
		return "", err
	}
	if recipient == "" || wamid == "" {
		return "", fmt.Errorf("recipient and wamid required")
	}
	req := metaMediaSendRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               recipient,
		Type:             "reaction",
		Reaction: &metaReactionPayload{
			MessageID: wamid,
			Emoji:     emoji,
		},
	}
	return s.doSend(ctx, recipient, "reaction", req)
}

// ─── Media download (inbound media bytes) ────────────────────────────────
//
// Meta hosts media inbound em CDN privada que expira em 5min. Pra Engine
// processar imagem/áudio/vídeo, Gateway precisa:
//
//   1. GET https://graph.facebook.com/{v}/{media_id}
//      → { url: "<signed_url>", mime_type, sha256, file_size, ... }
//   2. GET <signed_url> com Authorization: Bearer {token}
//      → bytes
//
// Resultado vai pro Engine via Vision/Audio MCP tools que aceitam tanto
// `meta_media_id` (downloaded server-side) quanto bytes diretos. POC: só
// expomos os primitivos; quem consome (worker, MCP) decide quando baixar.

// MetaMediaMetadata é o retorno do step 1 (metadata lookup).
type MetaMediaMetadata struct {
	URL          string `json:"url"`
	MimeType     string `json:"mime_type"`
	SHA256       string `json:"sha256"`
	FileSize     int64  `json:"file_size"`
	ID           string `json:"id"`
	MessagingProduct string `json:"messaging_product"`
}

// LookupMedia faz step 1 (metadata + signed URL). NÃO baixa bytes — caller
// decide se chama DownloadMedia(URL) ou se passa URL pro MCP fazer.
//
// `metaMediaID` é o `id` retornado pelo webhook em
// messages[i].image.id (ou audio.id, video.id, etc.).
func (s *MetaGraphService) LookupMedia(ctx context.Context, metaMediaID string) (*MetaMediaMetadata, error) {
	if err := s.credentialsReady(); err != nil {
		return nil, err
	}
	if metaMediaID == "" {
		return nil, fmt.Errorf("metaMediaID required")
	}
	url := fmt.Sprintf("https://graph.facebook.com/%s/%s", s.cfg.GraphAPIVersion, metaMediaID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create lookup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SystemUserToken)

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.logger.WithFields(logrus.Fields{
			"event":         "meta_media_lookup_error",
			"meta_media_id": metaMediaID,
			"duration_ms":   time.Since(start).Milliseconds(),
			"error":         err.Error(),
		}).Error("Meta media lookup failed")
		return nil, fmt.Errorf("lookup http: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "...(truncated)"
		}
		s.logger.WithFields(logrus.Fields{
			"event":         "meta_media_lookup_non_2xx",
			"meta_media_id": metaMediaID,
			"status_code":   resp.StatusCode,
			"error_body":    snippet,
		}).Error("Meta media lookup non-2xx")
		return nil, fmt.Errorf("media lookup status %d: %s", resp.StatusCode, snippet)
	}
	var meta MetaMediaMetadata
	if err := json.Unmarshal(body, &meta); err != nil {
		return nil, fmt.Errorf("parse media metadata: %w", err)
	}
	if meta.URL == "" {
		return nil, fmt.Errorf("media metadata missing url")
	}
	s.logger.WithFields(logrus.Fields{
		"event":         "meta_media_lookup_ok",
		"meta_media_id": metaMediaID,
		"mime_type":     meta.MimeType,
		"file_size":     meta.FileSize,
		"duration_ms":   time.Since(start).Milliseconds(),
	}).Info("Meta media lookup ok")
	return &meta, nil
}

// DownloadMedia faz step 2 (GET signed URL) e retorna os bytes. URL deve
// ter sido obtida via LookupMedia logo antes (TTL ~5min).
//
// Limite de tamanho: cap em `maxBytes` pra evitar DoS via media gigante.
// Passe 0 pra desabilitar cap (não recomendado em prod).
func (s *MetaGraphService) DownloadMedia(ctx context.Context, signedURL string, maxBytes int64) ([]byte, error) {
	if err := s.credentialsReady(); err != nil {
		return nil, err
	}
	if signedURL == "" {
		return nil, fmt.Errorf("signedURL required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SystemUserToken)
	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("media download status %d: %s", resp.StatusCode, string(body))
	}
	var reader io.Reader = resp.Body
	if maxBytes > 0 {
		reader = io.LimitReader(resp.Body, maxBytes+1)
	}
	bs, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if maxBytes > 0 && int64(len(bs)) > maxBytes {
		return nil, fmt.Errorf("media exceeds max bytes (%d > %d)", len(bs), maxBytes)
	}
	s.logger.WithFields(logrus.Fields{
		"event":       "meta_media_download_ok",
		"bytes":       len(bs),
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("Meta media downloaded")
	return bs, nil
}

// UploadMedia faz POST multipart pra `/{phone_number_id}/media` e retorna
// o `media_id` (handle reusável em SendMedia.ID). Usado quando Engine emite
// bytes inline (base64) que precisam ser servidos pelo Meta (sem URL pública).
//
// `mimeType` deve bater com `bytes` (Meta valida). `filename` é opcional —
// Meta gera um nome default. `messaging_product=whatsapp` é fixado no body.
//
// Limites Meta (referência v21.0):
//   - audio: 16 MB
//   - image: 5 MB
//   - video: 16 MB
//   - document: 100 MB
//
// Bytes maiores que o cap ainda enviam (Meta retorna 4xx). Gateway não pre-
// valida pra evitar duplicação — caller pode comparar `len(bytes)` antes.
func (s *MetaGraphService) UploadMedia(ctx context.Context, mimeType string, content []byte, filename string) (string, error) {
	if err := s.credentialsReady(); err != nil {
		return "", err
	}
	if mimeType == "" {
		return "", fmt.Errorf("mimeType required")
	}
	if len(content) == 0 {
		return "", fmt.Errorf("content empty")
	}
	if filename == "" {
		filename = "upload" // Meta aceita default; usado só pra debug header
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("messaging_product", "whatsapp")
	_ = writer.WriteField("type", mimeType)
	// CreateFormFile usa application/octet-stream por default. Meta exige
	// que o multipart file part tenha Content-Type igual ao `type` field;
	// caso contrário, upload é rejeitado. Construir o MIMEHeader manualmente.
	mh := make(textproto.MIMEHeader)
	mh.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	mh.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(mh)
	if err != nil {
		return "", fmt.Errorf("multipart create file part: %w", err)
	}
	if _, err := part.Write(content); err != nil {
		return "", fmt.Errorf("multipart write content: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("multipart close: %w", err)
	}

	url := fmt.Sprintf(
		"https://graph.facebook.com/%s/%s/media",
		s.cfg.GraphAPIVersion, s.cfg.PhoneNumberID,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return "", fmt.Errorf("create upload request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.SystemUserToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	start := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.logger.WithError(err).WithFields(logrus.Fields{
			"event":       "meta_media_upload_error",
			"mime_type":   mimeType,
			"bytes":       len(content),
			"duration_ms": time.Since(start).Milliseconds(),
		}).Error("Meta media upload failed")
		return "", fmt.Errorf("upload http: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(respBody)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "...(truncated)"
		}
		return "", fmt.Errorf("media upload status %d: %s", resp.StatusCode, snippet)
	}
	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parse upload response: %w", err)
	}
	if parsed.ID == "" {
		return "", fmt.Errorf("upload response missing id")
	}
	s.logger.WithFields(logrus.Fields{
		"event":       "meta_media_upload_ok",
		"media_id":    parsed.ID,
		"mime_type":   mimeType,
		"bytes":       len(content),
		"duration_ms": time.Since(start).Milliseconds(),
	}).Info("Meta media uploaded")
	return parsed.ID, nil
}

// doSend é o transport comum: serializa, POST, parse wamid.
// Espelha SendText mas é genérico (qualquer metaMediaSendRequest).
func (s *MetaGraphService) doSend(ctx context.Context, recipient, kind string, req metaMediaSendRequest) (string, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}
	url := fmt.Sprintf(
		"https://graph.facebook.com/%s/%s/messages",
		s.cfg.GraphAPIVersion, s.cfg.PhoneNumberID,
	)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+s.cfg.SystemUserToken)
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := s.httpClient.Do(httpReq)
	if err != nil {
		s.logger.WithFields(logrus.Fields{
			"event":            "meta_graph_send_error",
			"kind":             kind,
			"recipient_prefix": recipientPrefix(recipient),
			"duration_ms":      time.Since(start).Milliseconds(),
			"error":            err.Error(),
		}).Error("Meta Graph send failed")
		return "", fmt.Errorf("http send: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	logFields := logrus.Fields{
		"event":            "meta_graph_send",
		"kind":             kind,
		"recipient_prefix": recipientPrefix(recipient),
		"status_code":      resp.StatusCode,
		"duration_ms":      time.Since(start).Milliseconds(),
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errSnippet := string(respBody)
		if len(errSnippet) > 200 {
			errSnippet = errSnippet[:200] + "...(truncated)"
		}
		logFields["error_body"] = errSnippet
		s.logger.WithFields(logFields).Error("Meta Graph send non-2xx")
		return "", fmt.Errorf("meta returned status %d: %s", resp.StatusCode, errSnippet)
	}

	var sendResp MetaSendResponse
	if err := json.Unmarshal(respBody, &sendResp); err != nil {
		s.logger.WithFields(logFields).WithError(err).Error("Meta Graph response parse failed")
		return "", fmt.Errorf("parse response: %w", err)
	}
	if len(sendResp.Messages) == 0 || sendResp.Messages[0].ID == "" {
		s.logger.WithFields(logFields).Warn("Meta Graph success response without wamid")
		return "", fmt.Errorf("no wamid in response")
	}
	wamid := sendResp.Messages[0].ID
	logFields["wamid"] = wamid
	s.logger.WithFields(logFields).Info("Meta Graph send success")
	return wamid, nil
}
