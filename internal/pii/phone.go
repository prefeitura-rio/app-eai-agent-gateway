// Package pii concentra a redação de dados pessoais de cidadãos (LGPD) antes
// de eles chegarem a logs e atributos de telemetria.
package pii

// MaskPhone mascara um número de telefone para log, mantendo só o prefixo
// (país, DDD e os primeiros dígitos) para correlação.
//
// Exemplo: 5521999999999 -> 5521999***; valores com menos de 8 caracteres
// viram "****".
//
// Use-o em todo campo de log ou atributo de span que carregue o telefone do
// cidadão: user_number e o thread ID do Google Agent Engine, que é o próprio
// telefone.
func MaskPhone(phone string) string {
	if len(phone) < 8 {
		return "****"
	}
	return phone[:len(phone)-6] + "***"
}
