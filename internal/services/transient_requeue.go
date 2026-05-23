package services

import "time"

// TransientRequeueError sinaliza que um handler de mensagem encontrou
// uma condição esperada que justifica republish na própria fila, mas
// NÃO conta como falha de processamento — ou seja, não incrementa o
// retry counter nem aproxima a mensagem do DLQ.
//
// Caso de uso canônico: phone-lock contention quando duas mensagens do
// mesmo cidadão chegam concorrentemente. Lock contention é fluxo normal
// (operação serializada per-user), não falha de Engine/Redis.
//
// O consumer reconhece via errors.As e republica com o mesmo
// retryCount + delay sugerido pelo handler.
type TransientRequeueError interface {
	error
	TransientRequeueDelay() time.Duration
}
