package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// daquiA10min é a validade futura que um pareamento por QR deixa gravada.
var daquiA10min = time.Now().Add(10 * time.Minute)

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
	// pendingPairTokenExpiresAt modela o CASE do RETURNING real. Sem isso o
	// fake entregava o token SEMPRE, e um teste verde escondeu por meses o bug
	// de o pareamento por QR nunca funcionar em produção: a coluna nunca era
	// gravada, e em SQL `NULL > now()` é NULL, então o token nunca saía.
	pendingPairTokenExpiresAt *time.Time
	screenshotRequested       bool
	lockRequested             bool
	restartRequested          bool
	shutdownRequested         bool
	commandID                 *string
	commandText               *string
	secretHash                *string
	migrationBlocked          bool
	sshPublicKey              *string
	diagnosticNote            *string
	hostname                  *string
	cpuPercent                *float64
	ramPercent                *float64
	gpuPercent                *float64
	gpuName                   *string
	// metricsBumps conta quantas vezes metrics_at avançou — é o que prova que
	// um heartbeat sem métrica não finge leitura nova.
	metricsBumps int
	// Campos duráveis, lidos antes de decidir se vale escrever. pisoVencido
	// simula o "last_seen_at mais velho que o piso de frescor".
	appVersion       *string
	currentSessionID *string
	pisoVencido      bool

	upserts    int
	clears     int
	mints      int
	migrations int
	leituras   int
}

type fakeLabDeviceRow struct {
	db   *fakeLabDeviceDB
	kind fakeLabDeviceScan
	err  error
}

// fakeLabDeviceScan é a forma da linha devolvida por cada comando: o
// lookup/mint do segredo (1 coluna), o RETURNING do upsert (11) e a leitura
// que decide se vale escrever (17).
type fakeLabDeviceScan int

const (
	scanSecret fakeLabDeviceScan = iota
	scanHeartbeat
	scanLeitura
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
	// Mesmo CASE do SQL: token só sai se a validade existir E for futura.
	if r.db.pendingPairTokenExpiresAt != nil && r.db.pendingPairTokenExpiresAt.After(time.Now()) {
		*(dest[4].(**string)) = r.db.pendingPairToken
	} else {
		*(dest[4].(**string)) = nil
	}
	*(dest[5].(*bool)) = r.db.screenshotRequested
	*(dest[6].(*bool)) = r.db.lockRequested
	*(dest[7].(*bool)) = r.db.restartRequested
	*(dest[8].(*bool)) = r.db.shutdownRequested
	*(dest[9].(**string)) = r.db.commandID
	*(dest[10].(**string)) = r.db.commandText
	if r.kind == scanLeitura {
		// coalesce(...,'') no SQL real: NULL vira string vazia.
		*(dest[11].(*string)) = deref(r.db.appVersion)
		*(dest[12].(*string)) = deref(r.db.hostname)
		*(dest[13].(*string)) = deref(r.db.sshPublicKey)
		*(dest[14].(*string)) = deref(r.db.diagnosticNote)
		*(dest[15].(*string)) = deref(r.db.currentSessionID)
		*(dest[16].(*bool)) = r.db.pisoVencido
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (f *fakeLabDeviceDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "piso_vencido"):
		if !f.exists {
			return fakeLabDeviceRow{err: pgx.ErrNoRows}
		}
		f.leituras++
		return fakeLabDeviceRow{db: f, kind: scanLeitura}

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
		// Mesma semântica do CASE WHEN $6 = '' no SQL real: string vazia mantém
		// a chave já registrada, só sobrescreve quando vem algo de verdade.
		if key, _ := args[5].(string); key != "" {
			f.sshPublicKey = &key
		}
		if note, _ := args[6].(string); note != "" {
			f.diagnosticNote = &note
		}
		if host, _ := args[7].(string); host != "" {
			f.hostname = &host
		}
		// coalesce($9, coluna): valor nil mantém o anterior.
		if cpu, _ := args[8].(*float64); cpu != nil {
			f.cpuPercent = cpu
		}
		if ram, _ := args[9].(*float64); ram != nil {
			f.ramPercent = ram
		}
		if gpu, _ := args[10].(*float64); gpu != nil {
			f.gpuPercent = gpu
		}
		if name, _ := args[11].(string); name != "" {
			f.gpuName = &name
		}
		if hasMetrics, _ := args[12].(bool); hasMetrics {
			f.metricsBumps++
		}
		// O SET do ON CONFLICT só mexe em last_seen_at/last_ip/app_version/
		// current_session_id — as colunas de comando ficam como estavam, e é
		// isso que o RETURNING devolve.
		return fakeLabDeviceRow{db: f, kind: scanHeartbeat}
	}
	return fakeLabDeviceRow{err: pgx.ErrNoRows}
}

func (f *fakeLabDeviceDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "SET device_uuid =") {
		f.migrations++
		if f.migrationBlocked {
			// A identidade nova já tem registro próprio: o WHERE NOT EXISTS não
			// casa e nada é movido.
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}
	if !strings.Contains(sql, "UPDATE hour_lab_devices SET unpair_requested = false") {
		return pgconn.CommandTag{}, nil
	}
	f.clears++
	if f.unpairRequested || f.pendingPairToken != nil || f.screenshotRequested ||
		f.lockRequested || f.restartRequested || f.shutdownRequested {
		f.unpairRequested = false
		f.pendingPairToken = nil
		f.screenshotRequested = false
		f.lockRequested = false
		f.restartRequested = false
		f.shutdownRequested = false
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
		exists:                    true,
		name:                      ptrTo("PC-01"),
		unpairRequested:           true,
		pendingPairToken:          ptrTo("tok-secreto"),
		pendingPairTokenExpiresAt: &daquiA10min,
		secretHash:                ptrTo(sha256Hex(segredo)),
	}
	ctx := context.Background()

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0"})
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

	second, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0"})
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
	if _, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0"}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if fake.clears != 0 {
		t.Errorf("%d UPDATEs de limpeza sem comando pendente, quer 0", fake.clears)
	}
}

// TestHeartbeatLockRestartShutdownEntregamUmaVezSo é a mesma regressão de
// TestHeartbeatEntregaComandosUmaVezSo, aplicada aos comandos remotos (travar
// tela / reiniciar / desligar) — a rota que os admite (POST .../lock etc.)
// nunca tinha existido no servidor antes de 03/09/2026, então isto também
// cobre a entrega desses três pela primeira vez.
func TestHeartbeatLockRestartShutdownEntregamUmaVezSo(t *testing.T) {
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{
		exists: true, name: ptrTo("PC-03"), secretHash: ptrTo(sha256Hex(segredo)),
		lockRequested: true, restartRequested: true, shutdownRequested: true,
	}
	ctx := context.Background()

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0"})
	if err != nil {
		t.Fatalf("primeiro heartbeat: %v", err)
	}
	if !first.LockRequested || !first.RestartRequested || !first.ShutdownRequested {
		t.Errorf("primeiro heartbeat: lock=%v restart=%v shutdown=%v, queria os 3 true",
			first.LockRequested, first.RestartRequested, first.ShutdownRequested)
	}

	second, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0"})
	if err != nil {
		t.Fatalf("segundo heartbeat: %v", err)
	}
	if second.LockRequested || second.RestartRequested || second.ShutdownRequested {
		t.Errorf("segundo heartbeat: lock=%v restart=%v shutdown=%v, queria os 3 false (limpeza não rodou)",
			second.LockRequested, second.RestartRequested, second.ShutdownRequested)
	}
}

// TestHeartbeatCommandNaoEZeradoNaEntrega prova que command_id/command_text
// (diferente de unpair/lock/restart/shutdown) continuam voltando em TODO
// heartbeat depois de entregues — o PC deduplica pelo id, porque o resultado
// só chega bem depois de o comando terminar de rodar.
func TestHeartbeatCommandNaoEZeradoNaEntrega(t *testing.T) {
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{
		exists: true, name: ptrTo("PC-04"), secretHash: ptrTo(sha256Hex(segredo)),
		commandID: ptrTo("cmd-1"), commandText: ptrTo("Get-Process"),
	}
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		res, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0"})
		if err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
		if res.CommandID == nil || *res.CommandID != "cmd-1" || res.CommandText == nil || *res.CommandText != "Get-Process" {
			t.Errorf("heartbeat %d: commandId=%v commandText=%v, queria \"cmd-1\"/\"Get-Process\" nos dois",
				i, res.CommandID, res.CommandText)
		}
	}
	if fake.clears != 0 {
		t.Errorf("%d UPDATEs de limpeza — comando não deveria disparar limpeza nenhuma", fake.clears)
	}
}

// TestHeartbeatSSHPublicKeyGravaUmaVezSoNaoApagaComVazio prova a mesma
// semântica do open_apps aplicada à chave SSH: o autounattend.xml manda a
// chave só no primeiro heartbeat (0.1.x+); heartbeats seguintes chegam com
// sshPublicKey vazio e não podem apagar o que já foi registrado.
func TestHeartbeatSSHPublicKeyGravaUmaVezSoNaoApagaComVazio(t *testing.T) {
	const segredo = "segredo-do-pc"
	const chave = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINyOZHpPHJgaA8nHVVEpllq4I4UCyciydnsrbEWEUDFK guilherme@santostech"
	fake := &fakeLabDeviceDB{exists: false}
	ctx := context.Background()

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: "", IP: "1.2.3.4", AppVersion: "1.0.0", SSHPublicKey: chave})
	if err != nil {
		t.Fatalf("primeiro heartbeat: %v", err)
	}
	if fake.sshPublicKey == nil || *fake.sshPublicKey != chave {
		t.Fatalf("primeiro heartbeat: sshPublicKey = %v, quer %q", fake.sshPublicKey, chave)
	}

	segredoEmitido := *first.DeviceSecret
	second, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredoEmitido, IP: "1.2.3.4", AppVersion: "1.0.0"})
	if err != nil {
		t.Fatalf("segundo heartbeat: %v", err)
	}
	_ = second
	if fake.sshPublicKey == nil || *fake.sshPublicKey != chave {
		t.Errorf("segundo heartbeat (vazio): sshPublicKey = %v, quer manter %q", fake.sshPublicKey, chave)
	}
}

func TestHeartbeatDiagnosticNoteGravaUltimaNaoApagaComVazio(t *testing.T) {
	const segredo = "segredo-do-pc"
	const nota1 = "sshd=Running fw=True port22=OK"
	const nota2 = "sshd=Stopped fw=True port22=FAIL"
	fake := &fakeLabDeviceDB{exists: false}
	ctx := context.Background()

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: "", IP: "1.2.3.4", AppVersion: "1.0.0", DiagnosticNote: nota1})
	if err != nil {
		t.Fatalf("primeiro heartbeat: %v", err)
	}
	if fake.diagnosticNote == nil || *fake.diagnosticNote != nota1 {
		t.Fatalf("primeiro heartbeat: diagnosticNote = %v, quer %q", fake.diagnosticNote, nota1)
	}

	segredoEmitido := *first.DeviceSecret
	if _, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredoEmitido, IP: "1.2.3.4", AppVersion: "1.0.0", DiagnosticNote: nota2}); err != nil {
		t.Fatalf("segundo heartbeat: %v", err)
	}
	if fake.diagnosticNote == nil || *fake.diagnosticNote != nota2 {
		t.Errorf("segundo heartbeat: diagnosticNote = %v, quer atualizar pra %q", fake.diagnosticNote, nota2)
	}

	if _, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredoEmitido, IP: "1.2.3.4", AppVersion: "1.0.0"}); err != nil {
		t.Fatalf("terceiro heartbeat: %v", err)
	}
	if fake.diagnosticNote == nil || *fake.diagnosticNote != nota2 {
		t.Errorf("terceiro heartbeat (vazio): diagnosticNote = %v, quer manter %q", fake.diagnosticNote, nota2)
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

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: "", IP: "1.2.3.4", AppVersion: "1.0.0"})
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

	second, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: secret, IP: "1.2.3.4", AppVersion: "1.0.0"})
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
		exists:                    true,
		name:                      ptrTo("PC-01"),
		pendingPairToken:          ptrTo("tok-secreto"),
		pendingPairTokenExpiresAt: &daquiA10min,
		secretHash:                ptrTo(sha256Hex("segredo-do-pc")),
	}
	ctx := context.Background()

	for _, tentativa := range []string{"", "segredo-errado", sha256Hex("segredo-do-pc")} {
		res, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: tentativa, IP: "9.9.9.9", AppVersion: "1.0.0"})
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
		exists:                    true,
		name:                      ptrTo("PC-legado"),
		pendingPairToken:          ptrTo("tok-secreto"),
		pendingPairTokenExpiresAt: &daquiA10min,
	}
	res, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: "", IP: "1.2.3.4", AppVersion: "1.0.0"})
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

// A captura de tela é sob demanda: o comando sai UMA vez e o PC não fica
// tirando foto sozinho no heartbeat seguinte. Mesma garantia do unpair — e aqui
// ela importa mais, porque a tela de um PC compartilhado pode ter dado de quem
// está sentado nele.
func TestHeartbeatEntregaCapturaDeTelaUmaVezSo(t *testing.T) {
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{
		exists:              true,
		name:                ptrTo("PC-01"),
		screenshotRequested: true,
		secretHash:          ptrTo(sha256Hex(segredo)),
	}
	ctx := context.Background()

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0"})
	if err != nil {
		t.Fatalf("primeiro heartbeat: %v", err)
	}
	if !first.ScreenshotRequested {
		t.Fatal("primeiro heartbeat deveria pedir a captura")
	}

	second, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0"})
	if err != nil {
		t.Fatalf("segundo heartbeat: %v", err)
	}
	if second.ScreenshotRequested {
		t.Error("segundo heartbeat NÃO pode pedir captura de novo")
	}
}

// A lista de apps abertos é substituída a cada heartbeat, mas app antigo não
// manda nada — e "nada" não pode virar "nada aberto" numa máquina em uso.
func TestEncodeOpenAppsMantemAusenteDistintoDeVazio(t *testing.T) {
	if got := encodeOpenApps(nil); got != nil {
		t.Errorf("app que não mandou a lista deve virar nil (mantém a anterior), veio %q", got)
	}
	if got := encodeOpenApps([]string{}); string(got) != "[]" {
		t.Errorf("lista vazia explícita deve virar [] (nada aberto), veio %q", got)
	}
}

func TestEncodeOpenAppsLimpaELimita(t *testing.T) {
	// NUL vem do Windows em nomes de janela, igual ao inventário.
	got := encodeOpenApps([]string{"Chrome\x00", "  ", "Unity"})
	if string(got) != `["Chrome","Unity"]` {
		t.Fatalf("esperava nomes limpos e sem vazios, veio %q", got)
	}

	muitos := make([]string, maxOpenApps+50)
	for i := range muitos {
		muitos[i] = "app" + strconv.Itoa(i)
	}
	var decoded []string
	if err := json.Unmarshal(encodeOpenApps(muitos), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != maxOpenApps {
		t.Fatalf("%d apps gravados, teto é %d", len(decoded), maxOpenApps)
	}
}

// A identidade do PC deixou de morar só no config.json (0.1.10+): quando ela
// muda, o registro antigo é MOVIDO, não duplicado — foi o bug que fez o mesmo
// computador aparecer duas vezes no dashboard.
func TestMigracaoDeIdentidadeExigeOSegredoAntigo(t *testing.T) {
	const segredo = "segredo-do-pc"
	ctx := context.Background()

	// Com o segredo certo: migra, e o app é liberado a esquecer o id antigo.
	fake := &fakeLabDeviceDB{exists: true, name: ptrTo("PC-01"), secretHash: ptrTo(sha256Hex(segredo))}
	resolvido, err := migrateLabDeviceUUIDTx(ctx, fake, "antigo", "novo", segredo)
	if err != nil {
		t.Fatalf("migração com segredo certo: %v", err)
	}
	if fake.migrations != 1 || !resolvido {
		t.Errorf("%d migrações (resolvido=%v), quer 1 e true", fake.migrations, resolvido)
	}

	// Sem o segredo certo: NÃO migra. Saber o device_uuid (que fica visível num
	// QR na tela do PC) não pode bastar pra sequestrar o registro de outra
	// máquina. Fica PENDENTE: o app segue mandando o id antigo, e a migração sai
	// sozinha se o admin esquecer o segredo do registro antigo.
	outro := &fakeLabDeviceDB{exists: true, secretHash: ptrTo(sha256Hex("outro-segredo"))}
	resolvido, err = migrateLabDeviceUUIDTx(ctx, outro, "antigo", "novo", segredo)
	if err != nil {
		t.Fatalf("migração com segredo errado devolveu erro: %v", err)
	}
	if outro.migrations != 0 || resolvido {
		t.Errorf("segredo errado: %d migrações, resolvido=%v — quer 0 e false", outro.migrations, resolvido)
	}
}

// Se a identidade nova JÁ tem registro próprio, nada é movido — mas isso fica
// pendente, não resolvido: o app continua mandando o id antigo, e a migração
// acontece sozinha assim que o admin apagar o registro duplicado. É a diferença
// entre uma recuperação de dois cliques no dashboard e cirurgia no banco.
func TestMigracaoBloqueadaContinuaPendente(t *testing.T) {
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{exists: true, secretHash: ptrTo(sha256Hex(segredo)), migrationBlocked: true}
	resolvido, err := migrateLabDeviceUUIDTx(context.Background(), fake, "antigo", "novo", segredo)
	if err != nil {
		t.Fatalf("migração bloqueada devolveu erro: %v", err)
	}
	if resolvido {
		t.Error("migração bloqueada marcou resolvido — o app esqueceria o id antigo e a duplicata ficaria pra sempre")
	}
}

func TestMigracaoIgnoraCasosSemEfeito(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		old, new_, secret string
		querResolvido     bool
	}{
		// Nada a migrar: resolvido, senão o app mandaria o id antigo pra sempre.
		{"", "novo", "s", true},       // sem id antigo (instalação nova)
		{"igual", "igual", "s", true}, // mesma identidade
		// Registro antigo tem segredo e o app não apresentou nenhum: pendente.
		{"antigo", "novo", "", false},
	} {
		fake := &fakeLabDeviceDB{exists: true, secretHash: ptrTo(sha256Hex("s"))}
		resolvido, err := migrateLabDeviceUUIDTx(ctx, fake, c.old, c.new_, c.secret)
		if err != nil {
			t.Fatalf("caso %v: %v", c, err)
		}
		if fake.migrations != 0 {
			t.Errorf("caso %v migrou sem precisar", c)
		}
		if resolvido != c.querResolvido {
			t.Errorf("caso %v: resolvido=%v, quer %v", c, resolvido, c.querResolvido)
		}
	}
}

// Os PCs em 0.1.0 são anteriores ao segredo de dispositivo existir: nunca
// gravaram nenhum e não têm o que apresentar. Se a migração exigisse prova, o
// upgrade deles criaria um registro novo e deixaria o antigo — com nome e
// sessão em andamento — órfão, que é exatamente o que a migração existe pra
// evitar. Registro sem segredo migra livre, como authLabDeviceTx já faz na
// adoção: exigir prova aqui não protegeria nada, porque quem pudesse migrar
// poderia adotar direto e obter o mesmo controle.
func TestMigracaoDeRegistroSemSegredoNaoExigeProva(t *testing.T) {
	ctx := context.Background()
	fake := &fakeLabDeviceDB{exists: true, name: ptrTo("Pc gui"), secretHash: nil}
	resolvido, err := migrateLabDeviceUUIDTx(ctx, fake, "antigo", "novo", "")
	if err != nil {
		t.Fatalf("migração de registro sem segredo: %v", err)
	}
	if fake.migrations != 1 || !resolvido {
		t.Errorf("%d migrações (resolvido=%v), quer 1 e true", fake.migrations, resolvido)
	}

	// Hash vazio (em vez de NULL) conta como sem segredo pelo mesmo motivo.
	vazio := &fakeLabDeviceDB{exists: true, secretHash: ptrTo("")}
	resolvido, err = migrateLabDeviceUUIDTx(ctx, vazio, "antigo", "novo", "")
	if err != nil {
		t.Fatalf("migração com hash vazio: %v", err)
	}
	if vazio.migrations != 1 || !resolvido {
		t.Errorf("%d migrações com hash vazio (resolvido=%v), quer 1 e true", vazio.migrations, resolvido)
	}
}

// appDisplayName cobre os PCs que ainda mandam o caminho: app empacotado da
// Store devolve "C:\...\SnippingTool\SnippingTool.exe" no lugar do nome.
func TestAppDisplayNameResolveCaminhoDeAppDaStore(t *testing.T) {
	casos := map[string]string{
		`C:\Program Files\WindowsApps\Microsoft.ScreenSketch_11.2602.49.0_x64__8wekyb3d8bbwe\SnippingTool\SnippingTool.exe`: "SnippingTool",
		`C:/Users/x/app.EXE`: "app",
		"Zen":                "Zen",
		"NVIDIA App":         "NVIDIA App",
	}
	for entrada, esperado := range casos {
		if got := appDisplayName(entrada); got != esperado {
			t.Errorf("appDisplayName(%q) = %q, quer %q", entrada, got, esperado)
		}
	}
}

// ── métricas de uso (CPU/RAM/GPU) ────────────────────────────────────────────
//
// Quem manda é o watchdog da frota (script PowerShell, appVersion
// "wnsh-watchdog"), no MESMO heartbeat que já mandava diagnosticNote — não o
// hour-timer-app, que só roda em parte das máquinas.

func floatTo(v float64) *float64 { return &v }

func TestHeartbeatGravaMetricasEMantemQuandoNaoVem(t *testing.T) {
	fake := &fakeLabDeviceDB{exists: false}
	ctx := context.Background()

	first, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{
		DeviceUUID: "dev-uuid", IP: "1.2.3.4", AppVersion: "wnsh-watchdog",
		CPUPercent: floatTo(96), RAMPercent: floatTo(52), GPUPercent: floatTo(97),
		GPUName: "NVIDIA GeForce RTX 5060 Ti", Hostname: "gazake",
	})
	if err != nil {
		t.Fatalf("primeiro heartbeat: %v", err)
	}
	if fake.cpuPercent == nil || *fake.cpuPercent != 96 {
		t.Errorf("cpu = %v, quer 96", fake.cpuPercent)
	}
	if fake.gpuName == nil || *fake.gpuName != "NVIDIA GeForce RTX 5060 Ti" {
		t.Errorf("gpuName = %v", fake.gpuName)
	}
	if fake.hostname == nil || *fake.hostname != "gazake" {
		t.Errorf("hostname = %v, quer \"gazake\"", fake.hostname)
	}
	if fake.metricsBumps != 1 {
		t.Errorf("metrics_at avançou %d vezes, quer 1", fake.metricsBumps)
	}

	// Heartbeat sem métrica (script antigo, ou máquina sem GPU): mantém a
	// última leitura e NÃO finge que ela é de agora.
	segredo := *first.DeviceSecret
	if _, err := upsertLabDeviceHeartbeatTx(ctx, fake, labHeartbeat{
		DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "wnsh-watchdog",
	}); err != nil {
		t.Fatalf("segundo heartbeat: %v", err)
	}
	if fake.cpuPercent == nil || *fake.cpuPercent != 96 {
		t.Errorf("heartbeat sem métrica apagou a cpu: %v", fake.cpuPercent)
	}
	if fake.metricsBumps != 1 {
		t.Errorf("metrics_at avançou %d vezes; heartbeat sem métrica não pode avançar", fake.metricsBumps)
	}
}

// Um PC sem GPU manda só CPU e RAM — isso ainda conta como "veio métrica".
func TestHasMetricsSoPrecisaDeUma(t *testing.T) {
	if (labHeartbeat{}).HasMetrics() {
		t.Error("heartbeat sem nenhuma métrica não deveria contar como tendo")
	}
	if !(labHeartbeat{RAMPercent: floatTo(31)}).HasMetrics() {
		t.Error("só RAM já é métrica (máquina sem GPU)")
	}
}

// A rota é pública: lixo em porcentagem não pode virar 0% na tela, porque 0%
// é indistinguível de "máquina parada" e manda o admin investigar o errado.
func TestValidPercentDescartaForaDaFaixa(t *testing.T) {
	casos := []struct {
		nome string
		in   *float64
		quer bool // true = aceita
	}{
		{"nil", nil, false},
		{"zero", floatTo(0), true},
		{"cheio", floatTo(100), true},
		{"negativo", floatTo(-1), false},
		{"acima de 100", floatTo(101), false},
		{"NaN", floatTo(math.NaN()), false},
		{"infinito", floatTo(math.Inf(1)), false},
	}
	for _, c := range casos {
		got := validPercent(c.in)
		if (got != nil) != c.quer {
			t.Errorf("%s: aceito=%v, quer %v", c.nome, got != nil, c.quer)
		}
	}
}

// ── pareamento por QR: a validade do token ───────────────────────────────────

// O RETURNING do heartbeat só entrega o token quando
// `pending_pair_token_expires_at > now()`. Em SQL, `NULL > now()` é NULL — ou
// seja, deixar a coluna nula não "desliga a expiração", desliga a ENTREGA.
// Durante meses o UPDATE do pareamento gravava só o token, e o PC nunca o
// recebia: ficava na tela de pareamento com a sessão de horas já aberta e sem
// dono. Este teste é a guarda estática contra isso voltar.
func TestPareamentoGravaValidadeJuntoComOToken(t *testing.T) {
	if !strings.Contains(labDevicePairTokenSQL, "pending_pair_token_expires_at") {
		t.Fatal("o UPDATE do pareamento precisa gravar pending_pair_token_expires_at; sem isso o token nunca é entregue")
	}
	if !strings.Contains(labDevicePairTokenSQL, "pending_pair_token =") {
		t.Fatal("o UPDATE do pareamento precisa gravar o próprio token")
	}
}

// E a contraparte de comportamento: token sem validade não sai no heartbeat.
func TestHeartbeatNaoEntregaTokenSemValidade(t *testing.T) {
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{
		exists:           true,
		pendingPairToken: ptrTo("tok-sem-validade"),
		// pendingPairTokenExpiresAt deixado nil de propósito: é o estado que o
		// código quebrado produzia.
		secretHash: ptrTo(sha256Hex(segredo)),
	}
	res, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, labHeartbeat{
		DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PairToken != nil {
		t.Errorf("token sem validade não pode ser entregue, veio %q", *res.PairToken)
	}
}

// Token já vencido também não sai — a janela existe pra um pareamento nunca
// entregue não ressuscitar uma sessão velha horas depois.
func TestHeartbeatNaoEntregaTokenVencido(t *testing.T) {
	const segredo = "segredo-do-pc"
	vencido := time.Now().Add(-time.Minute)
	fake := &fakeLabDeviceDB{
		exists:                    true,
		pendingPairToken:          ptrTo("tok-vencido"),
		pendingPairTokenExpiresAt: &vencido,
		secretHash:                ptrTo(sha256Hex(segredo)),
	}
	res, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, labHeartbeat{
		DeviceUUID: "dev-uuid", DeviceSecret: segredo, IP: "1.2.3.4", AppVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PairToken != nil {
		t.Errorf("token vencido não pode ser entregue, veio %q", *res.PairToken)
	}
}

// ── escrita condicional: o volátil vai pro Redis, o Postgres só quando muda ──

func hbPadrao(secret string) labHeartbeat {
	return labHeartbeat{DeviceUUID: "dev-uuid", DeviceSecret: secret, IP: "1.2.3.4", AppVersion: "wnsh-watchdog"}
}

// O ponto da mudança: heartbeat que não traz nada durável novo NÃO escreve.
// Antes, todos os 1440 heartbeats diários por PC faziam UPDATE só porque
// last_seen_at muda.
func TestHeartbeatSemMudancaNaoEscreveNoPostgres(t *testing.T) {
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{
		exists: true, name: ptrTo("pc-meio-01"),
		secretHash:  ptrTo(sha256Hex(segredo)),
		appVersion:  ptrTo("wnsh-watchdog"),
		hostname:    ptrTo("PC-MEIO-01"),
		pisoVencido: false,
	}
	hb := hbPadrao(segredo)
	hb.Hostname = "PC-MEIO-01"
	// Métrica nova NÃO é motivo de escrita: ela vive no Redis.
	hb.CPUPercent = floatTo(42)
	hb.RAMPercent = floatTo(61)

	res, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, hb)
	if err != nil {
		t.Fatal(err)
	}
	if fake.upserts != 0 {
		t.Errorf("%d escritas no Postgres sem mudança durável, quer 0", fake.upserts)
	}
	if fake.leituras != 1 {
		t.Errorf("%d leituras, quer 1", fake.leituras)
	}
	if res.Name == nil || *res.Name != "pc-meio-01" {
		t.Errorf("o nome tem que vir mesmo sem escrever, veio %v", res.Name)
	}
}

// Quando vence o piso de frescor, escreve — é o que impede o Postgres de ficar
// arbitrariamente velho se o Redis sumir.
func TestHeartbeatEscreveQuandoPisoVence(t *testing.T) {
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{
		exists: true, secretHash: ptrTo(sha256Hex(segredo)),
		appVersion: ptrTo("wnsh-watchdog"), pisoVencido: true,
	}
	if _, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, hbPadrao(segredo)); err != nil {
		t.Fatal(err)
	}
	if fake.upserts != 1 {
		t.Errorf("%d escritas com piso vencido, quer 1", fake.upserts)
	}
}

// Campo durável que muda tem que escrever na hora, sem esperar o piso.
func TestHeartbeatEscreveQuandoCampoDuravelMuda(t *testing.T) {
	const segredo = "segredo-do-pc"
	casos := []struct {
		nome  string
		ajuda func(*labHeartbeat)
	}{
		{"versão do script", func(h *labHeartbeat) { h.AppVersion = "ativar-tailscale-ssh-frota-2.9" }},
		{"nome da máquina", func(h *labHeartbeat) { h.Hostname = "PC-RENOMEADO" }},
		{"chave SSH", func(h *labHeartbeat) { h.SSHPublicKey = "ssh-ed25519 AAAA nova" }},
		{"nota de diagnóstico", func(h *labHeartbeat) { h.DiagnosticNote = "sshd=Stopped" }},
		{"sessão pareada", func(h *labHeartbeat) { h.SessionID = ptrTo("sessao-nova") }},
	}
	for _, c := range casos {
		fake := &fakeLabDeviceDB{
			exists: true, secretHash: ptrTo(sha256Hex(segredo)),
			appVersion: ptrTo("wnsh-watchdog"), hostname: ptrTo("PC-MEIO-01"),
		}
		hb := hbPadrao(segredo)
		hb.Hostname = "PC-MEIO-01"
		c.ajuda(&hb)
		if _, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, hb); err != nil {
			t.Fatalf("%s: %v", c.nome, err)
		}
		if fake.upserts != 1 {
			t.Errorf("%s mudou e gerou %d escritas, quer 1", c.nome, fake.upserts)
		}
	}
}

// A entrega de comando NÃO pode depender de ter havido escrita: um PC parado,
// sem nada durável mudando, precisa receber unpair/aviso/pareamento igual.
func TestComandoEhEntregueMesmoSemEscrita(t *testing.T) {
	const segredo = "segredo-do-pc"
	fake := &fakeLabDeviceDB{
		exists: true, secretHash: ptrTo(sha256Hex(segredo)),
		appVersion:       ptrTo("wnsh-watchdog"),
		unpairRequested:  true,
		pendingPairToken: ptrTo("tok-secreto"), pendingPairTokenExpiresAt: &daquiA10min,
	}
	res, err := upsertLabDeviceHeartbeatTx(context.Background(), fake, hbPadrao(segredo))
	if err != nil {
		t.Fatal(err)
	}
	if fake.upserts != 0 {
		t.Errorf("não devia escrever: %d escritas", fake.upserts)
	}
	if !res.UnpairRequested {
		t.Error("unpair tem que ser entregue mesmo sem escrita")
	}
	if res.PairToken == nil || *res.PairToken != "tok-secreto" {
		t.Errorf("pairToken tem que ser entregue mesmo sem escrita, veio %v", res.PairToken)
	}
	if fake.clears != 1 {
		t.Errorf("%d limpezas, quer 1 — senão o comando é reentregue pra sempre", fake.clears)
	}
}

// Guarda estática: a leitura tem que devolver os comandos com as MESMAS
// expressões do RETURNING do upsert. Se uma das duas mudar sozinha, o PC passa
// a receber comando diferente conforme tenha havido escrita ou não.
func TestLeituraDoHeartbeatEspelhaOReturningDoUpsert(t *testing.T) {
	for _, expr := range []string{
		"pending_pair_token_expires_at > now()",
		"screenshot_requested_at IS NOT NULL",
		"unpair_requested",
		"message_id::text",
	} {
		if !strings.Contains(labDeviceHeartbeatReadSQL, expr) {
			t.Errorf("a leitura do heartbeat perdeu %q — divergiu do RETURNING do upsert", expr)
		}
		if !strings.Contains(labDeviceHeartbeatUpsertSQL, expr) {
			t.Errorf("o RETURNING do upsert perdeu %q — divergiu da leitura", expr)
		}
	}
}

// ── estado ao vivo (Redis) ───────────────────────────────────────────────────

func TestOverlayNaoApagaOQueORedisNaoTem(t *testing.T) {
	antes := time.Now().Add(-time.Hour)
	d := LabDevice{
		LastSeenAt: &antes,
		CPUPercent: floatTo(42),
		GPUName:    ptrTo("NVIDIA GeForce RTX 5060 Ti"),
		OpenApps:   []string{"Blender"},
		OpenAppsAt: &antes,
	}
	agora := time.Now()
	// Foto só com o heartbeat: sem métrica, sem lista de apps.
	labLiveState{LastSeenAt: &agora, LastIP: "100.64.1.2"}.overlay(&d)

	if d.LastSeenAt == nil || !d.LastSeenAt.Equal(agora) {
		t.Error("lastSeenAt tinha que ser atualizado")
	}
	if d.LastIP == nil || *d.LastIP != "100.64.1.2" {
		t.Errorf("lastIp = %v", d.LastIP)
	}
	if d.CPUPercent == nil || *d.CPUPercent != 42 {
		t.Error("métrica antiga não pode ser apagada por uma foto sem métrica")
	}
	if d.GPUName == nil {
		t.Error("nome da GPU não pode ser apagado")
	}
	if len(d.OpenApps) != 1 {
		t.Errorf("lista de apps não pode ser apagada por foto sem lista, veio %v", d.OpenApps)
	}
}

// last_seen mais VELHO no Redis que no Postgres não pode fazer o PC parecer
// mais offline do que está (acontece logo depois de uma escrita pelo piso).
func TestOverlayNaoRetrocedeOUltimoContato(t *testing.T) {
	agora := time.Now()
	antes := agora.Add(-time.Hour)
	d := LabDevice{LastSeenAt: &agora}
	labLiveState{LastSeenAt: &antes}.overlay(&d)
	if !d.LastSeenAt.Equal(agora) {
		t.Error("o overlay retrocedeu o último contato")
	}
}

func TestLiveStateDistingueListaAusenteDeVazia(t *testing.T) {
	agora := time.Now()

	semLista := liveStateFrom(labHeartbeat{IP: "1.2.3.4"}, agora)
	if semLista.OpenAppsAt != nil {
		t.Error("app que não mandou lista não pode carimbar openAppsAt (apagaria a anterior)")
	}

	vazia := liveStateFrom(labHeartbeat{IP: "1.2.3.4", OpenApps: []byte(`[]`)}, agora)
	if vazia.OpenAppsAt == nil {
		t.Error("lista vazia explícita significa 'nada aberto' e precisa carimbar")
	}

	comMetrica := liveStateFrom(labHeartbeat{CPUPercent: floatTo(10)}, agora)
	if comMetrica.MetricsAt == nil {
		t.Error("veio métrica, metricsAt tem que ser carimbado")
	}
	if liveStateFrom(labHeartbeat{}, agora).MetricsAt != nil {
		t.Error("sem métrica, metricsAt não pode avançar")
	}
}

// Sem Redis configurado (s.rdb nil) nada pode explodir — fail-open.
func TestEstadoAoVivoSemRedisNaoQuebra(t *testing.T) {
	s := &Server{}
	devices := []LabDevice{{DeviceUUID: "dev-uuid"}}
	s.applyLabLiveState(t.Context(), devices)
	s.saveLabLiveState(t.Context(), "dev-uuid", labLiveState{})
}
