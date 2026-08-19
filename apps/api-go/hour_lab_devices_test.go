package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeLabDeviceDB simula UMA linha de hour_lab_devices com a semântica real do
// Postgres para os dois comandos do heartbeat: o INSERT ... ON CONFLICT DO UPDATE
// não toca nas colunas de comando (então o RETURNING traz os valores antigos) e
// o UPDATE separado é quem de fato zera. Assim dá pra provar a entrega
// exatamente-uma-vez sem um Postgres real — o harness deste pacote roda com
// s.db == nil (ver nota em handlers_social_test.go).
type fakeLabDeviceDB struct {
	exists           bool
	name             *string
	unpairRequested  bool
	messageID        *string
	messageText      *string
	pendingPairToken *string

	upserts int
	clears  int
}

type fakeLabDeviceRow struct {
	db  *fakeLabDeviceDB
	err error
}

func (r fakeLabDeviceRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 5 {
		return pgx.ErrNoRows
	}
	*(dest[0].(**string)) = r.db.name
	*(dest[1].(*bool)) = r.db.unpairRequested
	*(dest[2].(**string)) = r.db.messageID
	*(dest[3].(**string)) = r.db.messageText
	*(dest[4].(**string)) = r.db.pendingPairToken
	return nil
}

func (f *fakeLabDeviceDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	if !strings.Contains(sql, "INSERT INTO hour_lab_devices") {
		return fakeLabDeviceRow{err: pgx.ErrNoRows}
	}
	f.upserts++
	f.exists = true
	// O SET do ON CONFLICT só mexe em last_seen_at/last_ip/app_version/
	// current_session_id — as colunas de comando ficam como estavam, e é isso
	// que o RETURNING devolve.
	return fakeLabDeviceRow{db: f}
}

func (f *fakeLabDeviceDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(sql, "UPDATE hour_lab_devices SET unpair_requested = false") {
		return pgconn.CommandTag{}, nil
	}
	f.clears++
	if f.unpairRequested || f.pendingPairToken != nil {
		f.unpairRequested = false
		f.pendingPairToken = nil
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func ptrTo(s string) *string { return &s }

// TestHeartbeatEntregaComandosUmaVezSo é a regressão do bug do CTE: antes,
// `upserted` (INSERT ... ON CONFLICT DO UPDATE) e `cleared` (UPDATE) modificavam
// a MESMA linha no MESMO comando — o Postgres aplica só uma das modificações e
// todos os sub-statements enxergam o mesmo snapshot, então a limpeza nunca
// rodava e o pairToken/unpair era reentregue em todo heartbeat.
func TestHeartbeatEntregaComandosUmaVezSo(t *testing.T) {
	fake := &fakeLabDeviceDB{
		exists:           true,
		name:             ptrTo("PC-01"),
		unpairRequested:  true,
		pendingPairToken: ptrTo("tok-secreto"),
	}
	ctx := context.Background()

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, "dev-uuid", "1.2.3.4", "1.0.0", nil)
	if err != nil {
		t.Fatalf("primeiro heartbeat: %v", err)
	}
	if !first.UnpairRequested {
		t.Error("primeiro heartbeat: unpairRequested deveria vir true")
	}
	if first.PairToken == nil || *first.PairToken != "tok-secreto" {
		t.Errorf("primeiro heartbeat: pairToken = %v, quer \"tok-secreto\"", first.PairToken)
	}
	if fake.clears != 1 {
		t.Errorf("primeiro heartbeat: %d UPDATEs de limpeza, quer 1", fake.clears)
	}

	second, err := upsertLabDeviceHeartbeatTx(ctx, fake, "dev-uuid", "1.2.3.4", "1.0.0", nil)
	if err != nil {
		t.Fatalf("segundo heartbeat: %v", err)
	}
	if second.UnpairRequested {
		t.Error("segundo heartbeat: unpairRequested foi reentregue (limpeza não rodou)")
	}
	if second.PairToken != nil {
		t.Errorf("segundo heartbeat: pairToken reentregue (%q) — limpeza não rodou", *second.PairToken)
	}
	if fake.clears != 1 {
		t.Errorf("segundo heartbeat: %d UPDATEs de limpeza no total, quer 1 (nada a limpar)", fake.clears)
	}
	if second.Name == nil || *second.Name != "PC-01" {
		t.Errorf("segundo heartbeat: name = %v, quer \"PC-01\" (não deve ser zerado)", second.Name)
	}
}

// TestHeartbeatNaoLimpaQuandoNaoHaComando evita um UPDATE inútil por heartbeat
// (~2/min por PC do laboratório) quando não há nada pendente.
func TestHeartbeatNaoLimpaQuandoNaoHaComando(t *testing.T) {
	fake := &fakeLabDeviceDB{exists: true, name: ptrTo("PC-02")}
	if _, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, "dev-uuid", "1.2.3.4", "1.0.0", nil); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if fake.clears != 0 {
		t.Errorf("%d UPDATEs de limpeza sem comando pendente, quer 0", fake.clears)
	}
}

// TestHeartbeatSQLNaoModificaDuasVezesNoMesmoComando é a guarda estática contra
// o bug voltar: o comando do upsert não pode conter o UPDATE de limpeza (dois
// modificadores da mesma linha no mesmo comando = a limpeza não roda).
func TestHeartbeatSQLNaoModificaDuasVezesNoMesmoComando(t *testing.T) {
	if strings.Contains(labDeviceHeartbeatUpsertSQL, "UPDATE hour_lab_devices") {
		t.Error("o comando do upsert voltou a embutir o UPDATE de limpeza (CTE) — o Postgres ignora a segunda modificação da mesma linha")
	}
	if !strings.Contains(labDeviceHeartbeatClearSQL, "pending_pair_token = NULL") {
		t.Error("o comando de limpeza não zera pending_pair_token")
	}
	if !strings.Contains(labDeviceHeartbeatClearSQL, "unpair_requested = false") {
		t.Error("o comando de limpeza não zera unpair_requested")
	}
}
