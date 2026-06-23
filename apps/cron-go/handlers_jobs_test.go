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
