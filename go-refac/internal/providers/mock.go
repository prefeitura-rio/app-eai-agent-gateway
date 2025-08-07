package providers

import (
	"context"
	"fmt"
	"time"

	"eai-gateway-go/pkg/schemas"
)

// NewMockProvider implementa Provider de forma previsível para validação do fluxo end-to-end.
func NewMockProvider(name string) Provider { return &mock{name: name} }

type mock struct{ name string }

func (m *mock) SendMessage(ctx context.Context, agentID string, message string, previousMessage *string) ([]schemas.Message, schemas.Usage, error) {
    // Simula latência
    time.Sleep(100 * time.Millisecond)
    messages := []schemas.Message{
        {Role: "user", Content: message},
        {Role: "ai", Content: fmt.Sprintf("[%s] resposta simulada", m.name)},
    }
    return messages, schemas.Usage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}, nil
}

func (m *mock) GetAgentID(ctx context.Context, userNumber string) (string, error) {
    // Para GAE, agentID == userNumber; para Letta, simulamos ausência para criar
    if m.name == "google_agent_engine" { return userNumber, nil }
    return "", nil
}

func (m *mock) CreateAgent(ctx context.Context, userNumber string, override map[string]any) (string, error) {
    return fmt.Sprintf("agent-%s", userNumber), nil
}

func (m *mock) DeleteAgent(ctx context.Context, agentID string, tags []string, deleteAll bool) error {
    return nil
}


