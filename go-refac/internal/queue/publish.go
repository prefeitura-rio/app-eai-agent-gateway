package queue

import (
	"context"
	"encoding/json"

	amqp "github.com/rabbitmq/amqp091-go"

	"eai-gateway-go/pkg/schemas"
)

// PublishUserMessage publica a mensagem do usuário para a rota do provider.
func (p *Publisher) PublishUserMessage(ctx context.Context, msg schemas.UserMessage) error {
    body, _ := json.Marshal(msg)
    rk := routingKeyFor(msg.Provider)
    return p.ch.PublishWithContext(ctx, p.cfg.RabbitExchange, rk, false, false, amqp.Publishing{
        ContentType:  "application/json",
        MessageId:    msg.MessageID,
        CorrelationId: msg.MessageID,
        DeliveryMode: amqp.Persistent,
        Body:         body,
        Headers:      amqp.Table{"attempt": msg.Attempt},
    })
}

// PublishAgentMessage publica mensagens destinadas ao provider Letta por agent_id.
func (p *Publisher) PublishAgentMessage(ctx context.Context, msg schemas.AgentMessage) error {
    body, _ := json.Marshal(msg)
    return p.ch.PublishWithContext(ctx, p.cfg.RabbitExchange, "provider.letta", false, false, amqp.Publishing{
        ContentType:  "application/json",
        MessageId:    msg.MessageID,
        CorrelationId: msg.MessageID,
        DeliveryMode: amqp.Persistent,
        Body:         body,
    })
}

func routingKeyFor(provider string) string {
    switch provider {
    case "letta":
        return "provider.letta"
    case "google_agent_engine":
        return "provider.gae"
    default:
        return "provider.gae"
    }
}


