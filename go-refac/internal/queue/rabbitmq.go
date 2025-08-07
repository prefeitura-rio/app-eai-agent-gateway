package queue

import (
	"context"
	"errors"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog/log"

	"eai-gateway-go/internal/config"
)

// Publisher publica mensagens nas rotas apropriadas por provider.
type Publisher struct {
    conn     *amqp.Connection
    ch       *amqp.Channel
    cfg      *config.Config
}

func NewPublisher(cfg *config.Config) (*Publisher, error) {
    conn, err := amqp.Dial(cfg.RabbitURL)
    if err != nil { return nil, err }
    ch, err := conn.Channel()
    if err != nil { conn.Close(); return nil, err }
    // Topologia básica
    if err := declareTopology(ch, cfg); err != nil { ch.Close(); conn.Close(); return nil, err }
    return &Publisher{conn: conn, ch: ch, cfg: cfg}, nil
}

func (p *Publisher) Close() {
    if p.ch != nil { _ = p.ch.Close() }
    if p.conn != nil { _ = p.conn.Close() }
}

// Consumer encapsula consumo com QoS e handler de mensagens.
type Consumer struct {
    conn *amqp.Connection
    ch   *amqp.Channel
    cfg  *config.Config
}

func NewConsumer(cfg *config.Config) (*Consumer, error) {
    conn, err := amqp.Dial(cfg.RabbitURL)
    if err != nil { return nil, err }
    ch, err := conn.Channel()
    if err != nil { conn.Close(); return nil, err }
    if err := declareTopology(ch, cfg); err != nil { ch.Close(); conn.Close(); return nil, err }
    if err := ch.Qos(cfg.RabbitPrefetch, 0, false); err != nil { ch.Close(); conn.Close(); return nil, err }
    return &Consumer{conn: conn, ch: ch, cfg: cfg}, nil
}

func (c *Consumer) Close() {
    if c.ch != nil { _ = c.ch.Close() }
    if c.conn != nil { _ = c.conn.Close() }
}

// Delivery fornece um wrapper simples com operações comuns.
type Delivery struct { amqp.Delivery }

func (d Delivery) Ack() error { return d.Delivery.Ack(false) }
func (d Delivery) Reject() error { return d.Delivery.Reject(false) }
func (d Delivery) Requeue() error { return d.Delivery.Reject(true) }

// Consume inicia consumo e chama handler para cada mensagem.
func (c *Consumer) Consume(ctx context.Context, queueName string, handler func(Delivery) error) error {
    msgs, err := c.ch.Consume(queueName, "", false, false, false, false, nil)
    if err != nil { return err }
    for {
        select {
        case <-ctx.Done():
            return nil
        case d, ok := <-msgs:
            if !ok { return errors.New("channel closed") }
            if err := handler(Delivery{d}); err != nil {
                log.Error().Err(err).Msg("handler error")
            }
        }
    }
}

func declareTopology(ch *amqp.Channel, cfg *config.Config) error {
    if err := ch.ExchangeDeclare(cfg.RabbitExchange, "topic", true, false, false, false, nil); err != nil {
        return err
    }
    // Filas por provider (sem DLQ neste esqueleto; pode-se evoluir com x-dead-letter)
    if _, err := ch.QueueDeclare(cfg.RabbitQueueLetta, true, false, false, false, nil); err != nil { return err }
    if _, err := ch.QueueDeclare(cfg.RabbitQueueGAE, true, false, false, false, nil); err != nil { return err }
    // Bindings
    if err := ch.QueueBind(cfg.RabbitQueueLetta, "provider.letta", cfg.RabbitExchange, false, nil); err != nil { return err }
    if err := ch.QueueBind(cfg.RabbitQueueGAE, "provider.gae", cfg.RabbitExchange, false, nil); err != nil { return err }
    return nil
}


