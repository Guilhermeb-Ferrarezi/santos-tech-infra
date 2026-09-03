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

	upserts    int
	clears     int
	mints      int
	migrations int
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
	// Mesmo CASE do SQL: token só sai se a validade existir E for futura.
	if r.db.pendingPairTokenExpiresAt != nil && r.db.pendingPairTokenExpiresAt.After(time.Now()) {
		*(dest[4].(**string)) = r.db.pendingPairToken
	} else {
		*(dest[4].(**string)) = nil
	}
	*(dest[5].(*bool)) = r.db.screenshotRequested
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
	if f.unpairRequested || f.pendingPairToken != nil || f.screenshotRequested {
		f.unpairRequested = false
		f.pendingPairToken = nil
		f.screenshotRequested = false
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
