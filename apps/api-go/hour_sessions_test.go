package main

import (
	"testing"
	"time"
)

func ev(kind string, at time.Time, delta int64) hourSessionEvent {
	return hourSessionEvent{EventType: kind, CreatedAt: at, DeltaSeconds: delta}
}

// O tempo da sessão é sempre derivado do histórico — este teste é a régua
// disso: soma os intervalos rodando e ignora os pausados.
func TestComputeElapsedSomaSoOTempoRodando(t *testing.T) {
	t0 := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	events := []hourSessionEvent{
		ev("start", t0, 0),
		ev("pause", t0.Add(30*time.Minute), 0),  // 30min rodando
		ev("resume", t0.Add(50*time.Minute), 0), // 20min parada, não conta
		ev("end", t0.Add(80*time.Minute), 0),    // +30min rodando
	}
	if got := computeElapsedSeconds(events, t0.Add(3*time.Hour)); got != 3600 {
		t.Fatalf("elapsed = %ds, quer 3600 (60min rodando)", got)
	}
}

// Sessão ainda aberta conta até agora — é o que faz o cronômetro andar.
func TestComputeElapsedContaAteAgoraQuandoAberta(t *testing.T) {
	t0 := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	events := []hourSessionEvent{ev("start", t0, 0)}
	if got := computeElapsedSeconds(events, t0.Add(90*time.Second)); got != 90 {
		t.Fatalf("elapsed = %ds, quer 90", got)
	}
}

// Ajuste manual entra na mesma conta dos intervalos: o total continua saindo
// do histórico, em vez de virar um número guardado à parte.
func TestComputeElapsedAplicaAjusteManual(t *testing.T) {
	t0 := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	base := []hourSessionEvent{ev("start", t0, 0), ev("end", t0.Add(time.Hour), 0)}

	somando := append(append([]hourSessionEvent{}, base...), ev("adjust", t0.Add(2*time.Hour), 600))
	if got := computeElapsedSeconds(somando, t0.Add(3*time.Hour)); got != 4200 {
		t.Fatalf("com +10min: %ds, quer 4200", got)
	}

	descontando := append(append([]hourSessionEvent{}, base...), ev("adjust", t0.Add(2*time.Hour), -600))
	if got := computeElapsedSeconds(descontando, t0.Add(3*time.Hour)); got != 3000 {
		t.Fatalf("com -10min: %ds, quer 3000", got)
	}
}

// Desconto maior que o tempo corrido não pode virar tempo negativo — o cliente
// não fica devendo tempo para a sessão.
func TestComputeElapsedNuncaFicaNegativo(t *testing.T) {
	t0 := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	events := []hourSessionEvent{
		ev("start", t0, 0),
		ev("end", t0.Add(10*time.Minute), 0),
		ev("adjust", t0.Add(20*time.Minute), -3600),
	}
	if got := computeElapsedSeconds(events, t0.Add(time.Hour)); got != 0 {
		t.Fatalf("elapsed = %ds, quer 0 (piso)", got)
	}
}

// Cliente com saldo suficiente pra cobrir a sessão inteira não gera tempo
// avulso — já pagou antecipado (billable = 0, não negativo).
func TestComputeBillableMinutesSaldoCobreTudo(t *testing.T) {
	if got := computeBillableMinutes(45, 60); got != 0 {
		t.Fatalf("billable = %d, quer 0 (saldo de 60min cobre os 45min usados)", got)
	}
}

// Saldo insuficiente: só o excedente é avulso, cobrável.
func TestComputeBillableMinutesSaldoParcial(t *testing.T) {
	if got := computeBillableMinutes(90, 30); got != 60 {
		t.Fatalf("billable = %d, quer 60 (90min usados - 30min de saldo)", got)
	}
}

// Cliente sem saldo nenhum (avulso puro): o tempo inteiro é cobrável.
func TestComputeBillableMinutesSemSaldo(t *testing.T) {
	if got := computeBillableMinutes(50, 0); got != 50 {
		t.Fatalf("billable = %d, quer 50 (sem saldo, tudo é avulso)", got)
	}
}

// Ajuste de zero ou fora de ±24h é recusado: o valor é digitado na mão e sai
// do saldo do cliente no encerramento, então um dígito a mais não pode passar.
func TestAdjustHourSessionRecusaValorForaDoLimite(t *testing.T) {
	s := &Server{}
	for _, delta := range []int64{0, maxAdjustSeconds + 1, -maxAdjustSeconds - 1} {
		if _, err := s.adjustHourSession(nil, "id", 1, delta, nil); err == nil {
			t.Errorf("delta %d deveria ser recusado", delta)
		}
	}
}
