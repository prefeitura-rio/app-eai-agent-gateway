package providers

// Factory seleciona o provider com base na string enviada no payload.
type Factory struct {
    letta Provider
    gae   Provider
}

func NewFactory() *Factory {
    // Para fins de refactor inicial, usamos mocks simples com contratos.
    // Em produção, substitua por clients reais (HTTP para Letta/GAE ou microserviço Python).
    return &Factory{
        letta: NewMockProvider("letta"),
        gae:   NewMockProvider("google_agent_engine"),
    }
}

func (f *Factory) Get(name string) Provider {
    switch name {
    case "letta":
        return f.letta
    case "google_agent_engine":
        return f.gae
    default:
        return f.gae
    }
}


