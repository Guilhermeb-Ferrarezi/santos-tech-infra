package main

import (
	"context"
	"errors"
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
	secretHash       *string

	upserts int
	clears  int
	mints   int
}

type fakeLabDeviceRow struct {
	db   *fakeLabDeviceDB
	kind fakeLabDeviceScan
	err  error
}

// fakeLabDeviceScan é a forma da linha devolvida por cada comando: 1 coluna
// (lookup/mint do segredo) ou 5 (upsert do heartbeat).
type fakeLabDeviceScan int

const (
	scanSecret fakeLabDeviceScan = iota
	scanHeartbeat
)

func (r fakeLabDeviceRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.kind == scanSecret {
		*(dest[0].(**string)) = r.db.secretHash
		return nil
	}
	*(dest[0].(**string)) = r.db.name
	*(dest[1].(*bool)) = r.db.unpairRequested
	*(dest[2].(**string)) = r.db.messageID
	*(dest[3].(**string)) = r.db.messageText
	*(dest[4].(**string)) = r.db.pendingPairToken
	return nil
}

func (f *fakeLabDeviceDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "FOR UPDATE"):
		if !f.exists {
			return fakeLabDeviceRow{err: pgx.ErrNoRows}
		}
		return fakeLabDeviceRow{db: f, kind: scanSecret}

	case strings.Contains(sql, "device_secret_hash = EXCLUDED.device_secret_hash"):
		// O WHERE do DO UPDATE só grava sobre linha sem segredo.
		if f.secretHash != nil && *f.secretHash != "" {
			return fakeLabDeviceRow{err: pgx.ErrNoRows}
		}
		f.mints++
		f.exists = true
		h := args[1].(string)
		f.secretHash = &h
		return fakeLabDeviceRow{db: f, kind: scanSecret}

	case strings.Contains(sql, "INSERT INTO hour_lab_devices"):
		f.upserts++
		f.exists = true
		// O SET do ON CONFLICT só mexe em last_seen_at/last_ip/app_version/
		// current_session_id — as colunas de comando ficam como estavam, e é
		// isso que o RETURNING devolve.
		return fakeLabDeviceRow{db: f, kind: scanHeartbeat}
	}
	return fakeLabDeviceRow{err: pgx.ErrNoRows}
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
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{
		exists:           true,
		name:             ptrTo("PC-01"),
		unpairRequested:  true,
		pendingPairToken: ptrTo("tok-secreto"),
		secretHash:       ptrTo(sha256Hex(segredo)),
	}
	ctx := context.Background()

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, "dev-uuid", segredo, "1.2.3.4", "1.0.0", nil)
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

	second, err := upsertLabDeviceHeartbeatTx(ctx, fake, "dev-uuid", segredo, "1.2.3.4", "1.0.0", nil)
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
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{exists: true, name: ptrTo("PC-02"), secretHash: ptrTo(sha256Hex(segredo))}
	if _, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, "dev-uuid", segredo, "1.2.3.4", "1.0.0", nil); err != nil {
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

// TestHeartbeatEmiteSegredoUmaVez cobre o dispositivo novo: o primeiro heartbeat
// cria o segredo e devolve em texto puro; do segundo em diante o segredo é
// exigido e nunca mais devolvido.
func TestHeartbeatEmiteSegredoUmaVez(t *testing.T) {
	fake := &fakeLabDeviceDB{}
	ctx := context.Background()

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, "dev-uuid", "", "1.2.3.4", "1.0.0", nil)
	if err != nil {
		t.Fatalf("primeiro heartbeat: %v", err)
	}
	if first.DeviceSecret == nil || *first.DeviceSecret == "" {
		t.Fatal("primeiro heartbeat de dispositivo novo não devolveu deviceSecret")
	}
	secret := *first.DeviceSecret
	if fake.secretHash == nil || *fake.secretHash != sha256Hex(secret) {
		t.Error("o segredo foi gravado em claro (ou não foi gravado) — tem que ser o sha256")
	}
	if fake.mints != 1 {
		t.Errorf("%d emissões de segredo, quer 1", fake.mints)
	}

	second, err := upsertLabDeviceHeartbeatTx(ctx, fake, "dev-uuid", secret, "1.2.3.4", "1.0.0", nil)
	if err != nil {
		t.Fatalf("segundo heartbeat com o segredo: %v", err)
	}
	if second.DeviceSecret != nil {
		t.Error("segundo heartbeat devolveu o segredo de novo")
	}
	if fake.mints != 1 {
		t.Errorf("%d emissões de segredo no total, quer 1", fake.mints)
	}
}

// TestHeartbeatSemSegredoNaoVazaPairToken é o cerne do furo original: bastava
// saber o device_uuid (exibido num QR na tela do PC) pra pedir o heartbeat e
// receber o token de pareamento em texto puro.
func TestHeartbeatSemSegredoNaoVazaPairToken(t *testing.T) {
	fake := &fakeLabDeviceDB{
		exists:           true,
		name:             ptrTo("PC-01"),
		pendingPairToken: ptrTo("tok-secreto"),
		secretHash:       ptrTo(sha256Hex("segredo-do-pc")),
	}
	ctx := context.Background()

	for _, tentativa := range []string{"", "segredo-errado", sha256Hex("segredo-do-pc")} {
		res, err := upsertLabDeviceHeartbeatTx(ctx, fake, "dev-uuid", tentativa, "9.9.9.9", "1.0.0", nil)
		if !errors.Is(err, errLabDeviceUnauthorized) {
			t.Fatalf("segredo %q: err = %v, quer errLabDeviceUnauthorized", tentativa, err)
		}
		if res != nil {
			t.Fatalf("segredo %q: devolveu resultado (%+v) num 401", tentativa, res)
		}
	}
	if fake.upserts != 0 {
		t.Errorf("%d upserts sem credencial — o heartbeat não autenticado não pode nem escrever last_ip", fake.upserts)
	}
	if fake.pendingPairToken == nil {
		t.Error("o token pendente foi zerado por uma requisição sem credencial")
	}
}

// TestHeartbeatAdocaoNaoEntregaPairToken: quem adota um device_uuid legado (sem
// segredo) não apresentou credencial nenhuma, então não recebe o token de
// pareamento nesse mesmo heartbeat.
func TestHeartbeatAdocaoNaoEntregaPairToken(t *testing.T) {
	fake := &fakeLabDeviceDB{
		exists:           true,
		name:             ptrTo("PC-legado"),
		pendingPairToken: ptrTo("tok-secreto"),
	}
	res, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, "dev-uuid", "", "1.2.3.4", "1.0.0", nil)
	if err != nil {
		t.Fatalf("adoção de legado: %v", err)
	}
	if res.DeviceSecret == nil {
		t.Fatal("adoção de legado não emitiu segredo")
	}
	if res.PairToken != nil {
		t.Errorf("adoção entregou pairToken (%q) pra quem não apresentou credencial", *res.PairToken)
	}
	if fake.pendingPairToken != nil {
		t.Error("o token pendente deveria ter sido zerado na adoção (admin repareia)")
	}
}
