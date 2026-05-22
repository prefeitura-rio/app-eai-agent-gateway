package workers

import (
	"fmt"
	"time"
)

// lockContentionRequeue implementa services.TransientRequeueError.
// Retornado quando o worker não conseguiu adquirir o phone-lock antes
// do timeout (lockTotalTimeout). Sinaliza ao consumer pra republicar
// na fila sem queimar retry budget — contention é serialização per-user
// esperada, não falha de processamento.
type lockContentionRequeue struct {
	userNumber string
}

func (e lockContentionRequeue) Error() string {
	return fmt.Sprintf("phone-lock contention for %s; transient requeue", e.userNumber)
}

// TransientRequeueDelay — 2s é curto o suficiente pra repor a mensagem
// pouco depois do lock holder esperado terminar, mas longo o suficiente
// pra evitar tight loop quando dois workers entram em lockstep. Bate com
// lockTotalTimeout (10s) — máx ~5 requeues por minuto por mensagem.
func (e lockContentionRequeue) TransientRequeueDelay() time.Duration {
	return 2 * time.Second
}
