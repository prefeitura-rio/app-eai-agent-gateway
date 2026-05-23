package handlers

import "testing"

func TestFlowRegistry_LongestFirstWins(t *testing.T) {
	// Armadilha histórica: substring match retornava first-declared. Com
	// registry "luz:eletrica;luminaria:reparo", flow_name "Luminária Quebrada"
	// casava "luz" primeiro → service errado. Longest-first resolve por
	// especificidade independente da ordem do operador.
	cases := []struct {
		name           string
		config         string
		flowName       string
		expectedSvc    string
	}{
		{
			name:        "longest entry wins independent of declaration order",
			config:      "luz:eletrica;luminaria:reparo_luminaria",
			flowName:    "Luminária Quebrada",
			expectedSvc: "reparo_luminaria",
		},
		{
			name:        "reverse declaration order — still longest wins",
			config:      "luminaria:reparo_luminaria;luz:eletrica",
			flowName:    "Luminária Quebrada",
			expectedSvc: "reparo_luminaria",
		},
		{
			name:        "diacritic + case insensitive match",
			config:      "luminaria:reparo",
			flowName:    "LUMINÁRIA",
			expectedSvc: "reparo",
		},
		{
			name:        "short entry still wins when only it matches",
			config:      "luz:eletrica;agua:saneamento",
			flowName:    "Luz da Rua",
			expectedSvc: "eletrica",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewFlowRegistry(tc.config, "fallback_svc")
			if got := r.Resolve(tc.flowName); got != tc.expectedSvc {
				t.Errorf("got %q, want %q", got, tc.expectedSvc)
			}
		})
	}
}

func TestFlowRegistry_DefaultWhenNothingMatches(t *testing.T) {
	r := NewFlowRegistry("luz:eletrica", "default_svc")
	if got := r.Resolve("Outra coisa qualquer"); got != "default_svc" {
		t.Errorf("got %q, want default_svc", got)
	}
}

func TestFlowRegistry_EmptyFlowNameReturnsDefault(t *testing.T) {
	r := NewFlowRegistry("luz:eletrica", "default_svc")
	if got := r.Resolve(""); got != "default_svc" {
		t.Errorf("expected default on empty flow_name, got %q", got)
	}
}

func TestFlowRegistry_NilSafe(t *testing.T) {
	var r *FlowRegistry
	if got := r.Resolve("anything"); got != "" {
		t.Errorf("nil receiver should return empty, got %q", got)
	}
}
