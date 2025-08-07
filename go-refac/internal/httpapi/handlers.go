package httpapi

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"eai-gateway-go/internal/config"
	"eai-gateway-go/internal/queue"
	"eai-gateway-go/internal/storage"
	"eai-gateway-go/pkg/schemas"
)

// Handler contém dependências necessárias aos endpoints.
type Handler struct {
    cfg       *config.Config
    store     *storage.RedisStore
    publisher *queue.Publisher
}

func NewHandler(cfg *config.Config, store *storage.RedisStore, pub *queue.Publisher) *Handler {
    return &Handler{cfg: cfg, store: store, publisher: pub}
}

// RegisterRoutes registra as rotas /api/v1 mantendo os contratos atuais.
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
    g := r.Group("/message")
    g.POST("/webhook/agent", h.AgentWebhook)
    g.POST("/webhook/user", h.UserWebhook)
    g.GET("/response", h.GetResponse)
}

// AgentWebhook enfileira mensagens para um agente Letta específico.
func (h *Handler) AgentWebhook(c *gin.Context) {
    var req schemas.AgentWebhookRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
        return
    }

    messageID := uuid.NewString()
    // status inicial para UX do polling
    _ = h.store.StoreProcessing(c.Request.Context(), messageID, 2*time.Minute)

    msg := schemas.AgentMessage{
        MessageID:  messageID,
        Provider:   "letta",
        AgentID:    req.AgentID,
        Message:    req.Message,
        Attempt:    1,
        MaxRetries: 3,
        CreatedAt:  time.Now().UTC(),
    }
    if err := h.publisher.PublishAgentMessage(c.Request.Context(), msg); err != nil {
        c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"detail": "enqueue failed"})
        return
    }

    c.JSON(http.StatusCreated, gin.H{
        "message_id": messageID,
        "status":     "processing",
        "polling_endpoint": "/api/v1/message/response?message_id=" + messageID,
    })
}

// UserWebhook aceita uma mensagem de usuário, determina provider e publica na fila.
func (h *Handler) UserWebhook(c *gin.Context) {
    var req schemas.UserWebhookRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"detail": err.Error()})
        return
    }

    if req.Provider == "" { req.Provider = "google_agent_engine" }

    messageID := uuid.NewString()
    _ = h.store.StoreProcessing(c.Request.Context(), messageID, 2*time.Minute)

    msg := schemas.UserMessage{
        MessageID:       messageID,
        Provider:        req.Provider,
        UserNumber:      req.UserNumber,
        Message:         req.Message,
        PreviousMessage: req.PreviousMessage,
        Attempt:         1,
        MaxRetries:      3,
        CreatedAt:       time.Now().UTC(),
    }
    if err := h.publisher.PublishUserMessage(c.Request.Context(), msg); err != nil {
        c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"detail": "enqueue failed"})
        return
    }

    c.JSON(http.StatusCreated, gin.H{
        "message_id": messageID,
        "status":     "processing",
        "polling_endpoint": "/api/v1/message/response?message_id=" + messageID,
    })
}

// GetResponse devolve o status e dados da mensagem via Redis, compatível com Python.
func (h *Handler) GetResponse(c *gin.Context) {
    messageID := c.Query("message_id")
    if messageID == "" {
        c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"detail": "message_id é obrigatório"})
        return
    }

    tr, found, err := h.store.GetResponse(c.Request.Context(), messageID)
    if err != nil {
        c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"detail": err.Error()})
        return
    }
    if !found {
        c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
            "status":     "not_found",
            "message":    "Message ID não encontrado ou expirado.",
            "message_id": messageID,
        })
        return
    }

    switch tr.Status {
    case "error":
        c.JSON(http.StatusInternalServerError, gin.H{
            "status":  "failed",
            "error":   tr.Error,
            "message": "Ocorreu um erro ao processar sua mensagem.",
        })
    case "retry":
        c.JSON(http.StatusAccepted, gin.H{
            "status":  "processing",
            "message": tr.Message,
        })
    case "processing":
        c.JSON(http.StatusAccepted, gin.H{
            "status":  "processing",
            "message": tr.Message,
        })
    case "done":
        c.JSON(http.StatusOK, gin.H{"status": "completed", "data": tr})
    default:
        c.JSON(http.StatusOK, gin.H{"status": "completed", "data": tr})
    }
}


