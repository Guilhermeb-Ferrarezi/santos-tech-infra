package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Estes testes travam invariantes de SQL que NÃO dá para exercitar sem Postgres
// (a suíte roda sem banco). Lendo o texto da query — na fonte sqlc e no código
// gerado, que é o que realmente executa — eles pegam a regressão exata que já
// custou dinheiro: expirar cobrança de cartão/boleto com a janela do QR do PIX,
// e não conseguir dar baixa numa cobrança que o job já tinha expirado.

// sqlOf extrai o corpo de uma query nomeada de um arquivo (fonte .sql ou gerado .go).
// O bloco vai do marcador "-- name: <nome> " até o próximo marcador "-- name:".
func sqlOf(t *testing.T, path, name string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("lendo %s: %v", path, err)
	}
	s := string(b)
	start := strings.Index(s, "-- name: "+name+" ")
	if start < 0 {
		t.Fatalf("query %s não encontrada em %s", name, path)
	}
	rest := s[start+len("-- name: "+name+" "):]
	if next := strings.Index(rest, "-- name: "); next >= 0 {
		rest = rest[:next]
	}
	return rest
}

// norm colapsa espaços em branco para comparar SQL sem depender da formatação.
var wsRe = regexp.MustCompile(`\s+`)

func norm(s string) string { return wsRe.ReplaceAllString(s, " ") }

// TestExpireOverdueChargesSoPix: a expiração por "vencimento + 23h" é a validade do QR
// do PIX. Cartão nasce com due_date = hoje (expirava em ~23h) e boleto liquida em D+1
// (já chegava expirado) — ambos precisam ficar de fora.
func TestExpireOverdueChargesSoPix(t *testing.T) {
	for _, path := range []string{"db/query/charges.sql", "db/charges.sql.go"} {
		q := norm(sqlOf(t, path, "ExpireOverdueCharges"))
		if !strings.Contains(q, "method = 'pix'") {
			t.Fatalf("%s: ExpireOverdueCharges precisa filtrar method = 'pix', veio: %s", path, q)
		}
		if !strings.Contains(q, "status = 'pending'") {
			t.Fatalf("%s: ExpireOverdueCharges deveria continuar só sobre pendentes: %s", path, q)
		}
	}
}

// TestMarkChargePaidAceitaExpirada: a confirmação vem do gateway (dinheiro já entrou).
// Casar só com status='pending' fazia a baixa virar no-op quando o job de expiração
// tinha passado antes — cobrança expirada no banco e paga na Efí.
func TestMarkChargePaidAceitaExpirada(t *testing.T) {
	for _, path := range []string{"db/query/charges.sql", "db/charges.sql.go"} {
		for _, name := range []string{"MarkChargePaid", "MarkChargePaidByProviderID"} {
			q := norm(sqlOf(t, path, name))
			if !strings.Contains(q, "status IN ('pending', 'expired')") {
				t.Fatalf("%s: %s precisa aceitar 'expired' vindo do gateway, veio: %s", path, name, q)
			}
			if !strings.Contains(q, "SET status = 'paid'") {
				t.Fatalf("%s: %s deveria marcar como paga: %s", path, name, q)
			}
		}
	}
}
