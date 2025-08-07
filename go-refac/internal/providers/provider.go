package providers

import (
	"context"
	"eai-gateway-go/pkg/schemas"
)

// Provider define as capacidades mínimas de um provedor de agentes.
type Provider interface {
    SendMessage(ctx context.Context, agentID string, message string, previousMessage *string) (messages []schemas.Message, usage schemas.Usage, err error)
    GetAgentID(ctx context.Context, userNumber string) (string, error)
    CreateAgent(ctx context.Context, userNumber string, override map[string]any) (string, error)
    DeleteAgent(ctx context.Context, agentID string, tags []string, deleteAll bool) error
}


