package main

import "testing"

func TestJobInputValidate(t *testing.T) {
	good := jobInput{Name: "x", ActionKind: "catalog", ActionRef: "payments.gerar-cobrancas-mes", ScheduleCron: "0 9 * * *", Timezone: "America/Sao_Paulo"}
	if msg, ok := good.validate(false); !ok {
		t.Fatalf("esperava válido, veio: %s", msg)
	}
	bad := jobInput{Name: "x", ActionKind: "http", ScheduleCron: "0 9 * * *", Timezone: "America/Sao_Paulo"}
	if _, ok := bad.validate(false); ok {
		t.Fatal("esperava rejeitar HTTP cru com allowRaw=false")
	}
}

func TestJobInputDefaultsClamp(t *testing.T) {
	// Valores absurdos devem ser limitados ao teto (evita goroutine/conexão presa).
	huge := jobInput{TimeoutSecs: 999999, MaxRetries: 100000}
	huge.defaults()
	if huge.TimeoutSecs != maxTimeoutSecs {
		t.Errorf("TimeoutSecs: esperava clamp para %d, veio %d", maxTimeoutSecs, huge.TimeoutSecs)
	}
	if huge.MaxRetries != maxRetriesCap {
		t.Errorf("MaxRetries: esperava clamp para %d, veio %d", maxRetriesCap, huge.MaxRetries)
	}

	// Valores válidos dentro do limite são preservados.
	ok := jobInput{TimeoutSecs: 60, MaxRetries: 5}
	ok.defaults()
	if ok.TimeoutSecs != 60 || ok.MaxRetries != 5 {
		t.Errorf("valores válidos não deveriam mudar: timeout=%d retries=%d", ok.TimeoutSecs, ok.MaxRetries)
	}

	// Zero/negativo cai no default.
	zero := jobInput{}
	zero.defaults()
	if zero.TimeoutSecs != 30 || zero.MaxRetries != 3 {
		t.Errorf("defaults errados: timeout=%d retries=%d", zero.TimeoutSecs, zero.MaxRetries)
	}
}
