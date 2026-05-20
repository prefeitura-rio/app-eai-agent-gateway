// Package models — Meta WhatsApp Business Cloud webhook payload shapes.
//
// Inspirado na documentação oficial Meta:
// https://developers.facebook.com/docs/whatsapp/cloud-api/webhooks/components
//
// Escopo do POC: text inbound + status callbacks + envelope. Tipos avançados
// (image/audio/video/location/interactive) vão ser adicionados em fases
// posteriores conforme migration plan do ADR (cortar Mule).
//
// Todos os campos `omitempty` porque Meta envia payloads heterogêneos — uma
// entrada pode ter apenas `messages`, outra apenas `statuses`. Parsing
// tolerante.
package models

// MetaWebhookEnvelope — shape externo do webhook (object + entry array).
type MetaWebhookEnvelope struct {
	Object string      `json:"object"`
	Entry  []MetaEntry `json:"entry"`
}

// MetaEntry — uma entrada do envelope, normalmente uma WABA + changes.
type MetaEntry struct {
	ID      string       `json:"id"`
	Changes []MetaChange `json:"changes"`
}

// MetaChange — single change pra um field específico (e.g. "messages").
type MetaChange struct {
	Field string          `json:"field"`
	Value MetaChangeValue `json:"value"`
}

// MetaChangeValue — payload real. Pode conter messages, statuses, contacts,
// errors. POC consome só messages + statuses.
type MetaChangeValue struct {
	MessagingProduct string          `json:"messaging_product"`
	Metadata         MetaMetadata    `json:"metadata"`
	Contacts         []MetaContact   `json:"contacts,omitempty"`
	Messages         []MetaMessage   `json:"messages,omitempty"`
	Statuses         []MetaStatus    `json:"statuses,omitempty"`
	Errors           []MetaErrorItem `json:"errors,omitempty"`
}

// MetaMetadata — sobre o WABA receptor.
type MetaMetadata struct {
	DisplayPhoneNumber string `json:"display_phone_number"`
	PhoneNumberID      string `json:"phone_number_id"`
}

// MetaContact — info do cidadão remetente.
type MetaContact struct {
	WaID    string          `json:"wa_id"`
	Profile MetaContactName `json:"profile"`
}

// MetaContactName — nome opcional do contato.
type MetaContactName struct {
	Name string `json:"name"`
}

// MetaMessage — mensagem inbound. Type discrimina shape (text, image, etc).
type MetaMessage struct {
	From      string           `json:"from"`
	ID        string           `json:"id"`
	Timestamp string           `json:"timestamp"`
	Type      string           `json:"type"`
	Text      *MetaText        `json:"text,omitempty"`
	Image     *MetaMedia       `json:"image,omitempty"`
	Audio     *MetaMedia       `json:"audio,omitempty"`
	Video     *MetaMedia       `json:"video,omitempty"`
	Document  *MetaMedia       `json:"document,omitempty"`
	Sticker   *MetaMedia       `json:"sticker,omitempty"`
	Location  *MetaLocation    `json:"location,omitempty"`
	// Interactive cobre button_reply, list_reply, nfm_reply (Flow submission).
	Interactive *MetaInteractive `json:"interactive,omitempty"`
	Reaction    *MetaReaction    `json:"reaction,omitempty"`
}

// MetaText — body do texto inbound.
type MetaText struct {
	Body string `json:"body"`
}

// MetaMedia — referência a binário hosted na Meta CDN.
// Bot precisa fazer GET /{media_id} pra obter URL temporária + GET nessa URL
// pra baixar o conteúdo. Mule fazia isso; Gateway terá que replicar quando
// suportar media inbound.
type MetaMedia struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Filename string `json:"filename,omitempty"` // só pra document/sticker às vezes
	Caption  string `json:"caption,omitempty"`  // image/video/document
}

// MetaLocation — coordenadas compartilhadas via "anexo → localização".
type MetaLocation struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name,omitempty"`
	Address   string  `json:"address,omitempty"`
}

// MetaInteractive — Flow/button/list reply.
type MetaInteractive struct {
	Type        string             `json:"type"`
	ButtonReply *MetaInteractiveID `json:"button_reply,omitempty"`
	ListReply   *MetaInteractiveID `json:"list_reply,omitempty"`
	NFMReply    *MetaNFMReply      `json:"nfm_reply,omitempty"`
}

// MetaInteractiveID — comum a button_reply/list_reply.
type MetaInteractiveID struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// MetaNFMReply — WhatsApp Flow completion.
type MetaNFMReply struct {
	ResponseJSON string `json:"response_json"`
	Body         string `json:"body,omitempty"`
	Name         string `json:"name"`
	FlowToken    string `json:"flow_token,omitempty"`
}

// MetaReaction — emoji em mensagem prévia.
type MetaReaction struct {
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji"`
}

// MetaStatus — callback de status (sent/delivered/read/failed).
type MetaStatus struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Timestamp   string `json:"timestamp"`
	RecipientID string `json:"recipient_id"`
}

// MetaErrorItem — erro propagado pelo Meta (ex: outbound message failed).
type MetaErrorItem struct {
	Code    int    `json:"code"`
	Title   string `json:"title"`
	Message string `json:"message,omitempty"`
}
