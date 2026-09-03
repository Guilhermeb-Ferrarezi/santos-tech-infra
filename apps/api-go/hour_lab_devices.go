package main

// PCs do laboratório rodando o app desktop (hour-timer-app). Cada instalação
// tem um device_uuid estável gerado no primeiro boot; o app manda heartbeat
// periódico e este arquivo faz a contraparte no banco. Nome é sempre
// atribuído pelo admin (nunca pelo próprio PC, ver hour_lab_devices no
// schema) — o app só exibe de volta o que o backend devolver.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type LabDevice struct {
	ID                string     `json:"id"`
	DeviceUUID        string     `json:"deviceUuid"`
	Name              *string    `json:"name"`
	LastSeenAt        *time.Time `json:"lastSeenAt"`
	LastIP            *string    `json:"lastIp"`
	AppVersion        *string    `json:"appVersion"`
	CurrentSessionID  *string    `json:"currentSessionId"`
	CurrentClientName *string    `json:"currentClientName"`
	CurrentStatus     *string    `json:"currentStatus"`
	// HasSecret diz se o PC já adotou um segredo de dispositivo. Enquanto for
	// false qualquer um que saiba o device_uuid (que fica visível no QR da tela)
	// consegue adotá-lo no primeiro heartbeat — o admin usa isto pra conferir
	// que todos os PCs já adotaram depois do rollout.
	HasSecret bool      `json:"hasSecret"`
	CreatedAt time.Time `json:"createdAt"`
	// SSHPublicKey: chave pública que a própria máquina mandou no primeiro
	// heartbeat (ver ssh_public_key no schema). nil = máquina não manda (imagem
	// mais antiga, ou não é um PC provisionado pelo autounattend.xml).
	SSHPublicKey *string `json:"sshPublicKey"`
	// DiagnosticNote: auto-diagnóstico opcional mandado junto do heartbeat por
	// scripts de instalação (não é o hour-timer-app) que não têm outro jeito de
	// reportar status pra quem não está com acesso físico/SSH à máquina no
	// momento. nil = ninguém mandou nada ainda.
	DiagnosticNote *string `json:"diagnosticNote"`
	// OpenApps: nomes dos aplicativos abertos no último heartbeat (0.1.9+).
	// Vazio = app antigo ou nada aberto; OpenAppsAt diz quando foi visto. O
	// primeiro da lista é o app da janela em foco — o que o dashboard destaca
	// como "em uso agora".
	OpenApps   []string   `json:"openApps"`
	OpenAppsAt *time.Time `json:"openAppsAt"`
	// OpenAppIcons: nome do app aberto → sha256 do PNG do ícone (URL em
	// /program-icons/{hash}). Preenchido na listagem cruzando o nome com o
	// inventário do PC (ver resolveOpenAppIcons) — a imagem já veio no
	// inventário, não num campo novo do heartbeat, então nada muda no app.
	// Sempre um objeto, nunca null: nome ausente = sem ícone conhecido, e o
	// dashboard cai num placeholder.
	OpenAppIcons map[string]string `json:"openAppIcons"`
	// Hostname: nome da máquina no Windows (o mesmo que vira hostname do nó no
	// Tailscale). Não substitui Name — serve de rótulo quando o admin ainda não
	// renomeou o PC, que é o caso comum logo depois de provisionar.
	Hostname *string `json:"hostname"`
	// Uso da máquina no último heartbeat que trouxe métricas. nil = o PC nunca
	// reportou (script antigo) — o dashboard mostra traço, nunca 0%.
	CPUPercent *float64 `json:"cpuPercent"`
	RAMPercent *float64 `json:"ramPercent"`
	GPUPercent *float64 `json:"gpuPercent"`
	GPUName    *string  `json:"gpuName"`
	// GPUPowerWatts: consumo da GPU em watts (nvidia-smi power.draw). Não tem
	// equivalente pra CPU/RAM por software — precisaria de medidor físico na
	// tomada — mas numa rig de mineração é a GPU que domina o consumo mesmo.
	GPUPowerWatts *float64   `json:"gpuPowerWatts"`
	MetricsAt     *time.Time `json:"metricsAt"`
	// LockRequestedAt/RestartRequestedAt/ShutdownRequestedAt: não-nil enquanto o
	// pedido não foi entregue no heartbeat (ver labDeviceHeartbeatClearSQL) —
	// o dashboard usa só pra saber que ainda está "em voo", a ação em si não
	// tem resultado a mostrar.
	LockRequestedAt     *time.Time `json:"lockRequestedAt"`
	RestartRequestedAt  *time.Time `json:"restartRequestedAt"`
	ShutdownRequestedAt *time.Time `json:"shutdownRequestedAt"`
	// CommandText/CommandSentAt: último comando livre mandado pro PC.
	// CommandResult/CommandResultAt: o que ele reportou de volta (ver POST
	// /public/lab-devices/command-result) — nil até o PC responder, e fica
	// nulo pra sempre se o PC nunca rodar (script antigo, ou PC desligado).
	CommandText     *string    `json:"commandText"`
	CommandSentAt   *time.Time `json:"commandSentAt"`
	CommandResult   *string    `json:"commandResult"`
	CommandResultAt *time.Time `json:"commandResultAt"`
}

// labHeartbeat é tudo que um heartbeat traz pro store gravar.
//
// Virou struct quando os campos passaram de dez: com parâmetros posicionais,
// trocar dois strings de lugar na chamada compilava numa boa e gravava, por
// exemplo, a nota de diagnóstico na coluna da chave SSH.
type labHeartbeat struct {
	DeviceUUID   string
	DeviceSecret string
	IP           string
	AppVersion   string
	SessionID    *string
	// OpenApps já vem serializado pra jsonb; nil = o app não mandou a lista, e
	// o SQL mantém a última conhecida em vez de apagar.
	OpenApps []byte
	// PreviousUUID: identidade anterior a migrar (ver migrateLabDeviceUUIDTx).
	PreviousUUID string
	// Campos "grava a última, vazio não apaga": o PC manda só quando tem.
	SSHPublicKey   string
	DiagnosticNote string
	Hostname       string
	GPUName        string
	// Métricas de uso; nil = não veio neste heartbeat (mantém a anterior).
	CPUPercent    *float64
	RAMPercent    *float64
	GPUPercent    *float64
	GPUPowerWatts *float64
}

// HasMetrics diz se este heartbeat trouxe alguma métrica — é o que decide se
// metrics_at avança. Sem isso, um PC que parou de reportar pareceria estar
// mandando uso novo a cada 60s.
func (h labHeartbeat) HasMetrics() bool {
	return h.CPUPercent != nil || h.RAMPercent != nil || h.GPUPercent != nil || h.GPUPowerWatts != nil
}

// LabDeviceHeartbeatResult é o que o app precisa saber a cada heartbeat: nome
// atribuído (pra exibir), e comandos pendentes do admin.
type LabDeviceHeartbeatResult struct {
	Name            *string
	UnpairRequested bool
	MessageID       *string
	MessageText     *string
	PairToken       *string
	// ScreenshotRequested: o admin pediu uma captura de tela; o app tira, MOSTRA
	// o aviso na tela do PC e manda em POST /public/lab-devices/screenshot.
	ScreenshotRequested bool
	// PreviousDeviceResolved diz que não sobrou nada a migrar do id anterior — o
	// app só ESQUECE o id antigo quando ouve isto, e reenvia previousDeviceId em
	// todo heartbeat até lá. Sem essa confirmação a migração seria um evento
	// único e irrepetível: um envio que chegasse na hora errada (servidor ainda
	// no build anterior, registro antigo travado) deixaria um dispositivo
	// duplicado pra sempre. Aconteceu de verdade no rollout da 0.1.10.
	PreviousDeviceResolved bool
	// DeviceSecret só vem preenchido no ÚNICO heartbeat em que o segredo é
	// criado (dispositivo novo, ou legado ainda sem segredo). O app tem que
	// gravar em disco: sem ele os heartbeats seguintes levam 401.
	DeviceSecret *string
	// LockRequested/RestartRequested/ShutdownRequested: entregues exatamente
	// uma vez (mesma semântica do UnpairRequested) — o app trava a tela,
	// reinicia ou desliga a máquina ao receber true.
	LockRequested     bool
	RestartRequested  bool
	ShutdownRequested bool
	// CommandID/CommandText: comando livre pendente. NÃO é zerado na entrega —
	// o app deduplica localmente pelo id (mesma ideia do MessageID), porque o
	// resultado (POST /public/lab-devices/command-result) só chega bem depois.
	CommandID   *string
	CommandText *string
}

var errLabDeviceNotFound = appErr(http.StatusNotFound, "LAB_DEVICE_NOT_FOUND", "Dispositivo não encontrado")

// errLabDeviceUnauthorized: o heartbeat é público (o PC não faz login), então o
// segredo de dispositivo é a única credencial. Sem ele nada do estado do
// dispositivo é lido nem escrito — em especial o pending_pair_token, que sai em
// texto puro e antes bastava saber o device_uuid (exibido num QR na tela do PC)
// pra roubar.
var errLabDeviceUnauthorized = appErr(http.StatusUnauthorized, "LAB_DEVICE_UNAUTHORIZED", "Segredo do dispositivo ausente ou inválido")

// labDeviceQuerier é o subconjunto de pgx.Tx que upsertLabDeviceHeartbeatTx usa.
// Existe pra o teste conseguir provar a entrega-exatamente-uma-vez sem um
// Postgres real (o harness de teste deste pacote roda com s.db == nil).
type labDeviceQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// labDeviceHeartbeatUpsertSQL grava/atualiza os campos de heartbeat e devolve os
// comandos pendentes. As colunas de comando (name, unpair_requested, message_*,
// pending_pair_token) NÃO entram no SET, então o RETURNING traz justamente os
// valores ANTIGOS — que é o que o app precisa receber.
//
// Antes isto era um único comando com dois CTEs (`upserted` fazendo
// INSERT...ON CONFLICT DO UPDATE e `cleared` fazendo UPDATE) sobre a MESMA
// linha: o Postgres aplica só uma das modificações por comando, e todos os
// sub-statements enxergam o mesmo snapshot — o `cleared` nunca tinha efeito e
// unpair_requested/pending_pair_token eram reentregues em TODO heartbeat,
// apesar do comentário prometer "entregue exatamente uma vez".
// pairTokenTTL é a validade do token deixado pendente pelo pareamento via QR
// (pairLabDeviceViaQR). Pareamento por QR é uma ação ao vivo — admin escaneia
// e confirma na hora — então 10min é folgado pro caso comum e ainda fecha a
// janela pra um token nunca entregue (app ficou off, rede caiu, etc.)
// reaparecer do nada horas/dias depois e reabrir uma sessão que já devia ter
// ficado pra trás. Cada sessão é de uso único por design (ver hour_sessions):
// um token velho ressuscitando uma sessão antiga quebra essa regra.
const pairTokenTTL = 10 * time.Minute

// labDevicePairTokenSQL deixa o token pronto pra entrega no próximo heartbeat.
//
// A validade PRECISA ser gravada junto com o token. O RETURNING do heartbeat só
// entrega quando `pending_pair_token_expires_at > now()`, e em SQL
// `NULL > now()` é NULL — ou seja, deixar a coluna nula não "desliga a
// expiração", desliga a ENTREGA. Enquanto este UPDATE gravava só o token, o
// pareamento por QR nunca funcionou: o PC seguia na tela de pareamento, a
// sessão de horas ficava aberta sem dono, e o token em texto puro permanecia no
// banco (a limpeza só roda quando algum comando é entregue). Ver
// TestPareamentoGravaValidadeJuntoComOToken.
const labDevicePairTokenSQL = `
	UPDATE hour_lab_devices
	SET pending_pair_token = $2, pending_pair_token_expires_at = now() + make_interval(secs => $3)
	WHERE device_uuid = $1`

// labDeviceHeartbeatReadSQL lê, numa tacada, o que o heartbeat precisa
// DEVOLVER (os comandos pendentes) e o que decide se vale a pena ESCREVER (os
// campos duráveis atuais e se o piso de frescor venceu).
//
// As expressões dos comandos são idênticas às do RETURNING do upsert de
// propósito: como o SET do upsert não toca nas colunas de comando, ler antes
// dá exatamente o mesmo valor que o RETURNING daria. Ver
// TestLeituraDoHeartbeatEspelhaOReturningDoUpsert.
const labDeviceHeartbeatReadSQL = `
	SELECT name, unpair_requested, message_id::text, message_text,
		CASE WHEN pending_pair_token_expires_at > now() THEN pending_pair_token ELSE NULL END,
		screenshot_requested_at IS NOT NULL,
		lock_requested_at IS NOT NULL, restart_requested_at IS NOT NULL, shutdown_requested_at IS NOT NULL,
		command_id::text, command_text,
		coalesce(app_version, ''), coalesce(hostname, ''), coalesce(ssh_public_key, ''),
		coalesce(diagnostic_note, ''), coalesce(current_session_id::text, ''),
		(last_seen_at IS NULL OR last_seen_at < now() - make_interval(secs => $2)) AS piso_vencido
	FROM hour_lab_devices WHERE device_uuid = $1`

// sessionIDText normaliza o id de sessão pra comparar com o que veio do banco
// (que usa string vazia no lugar de NULL, via coalesce).
func sessionIDText(id *string) string {
	if id == nil {
		return ""
	}
	return *id
}

const labDeviceHeartbeatUpsertSQL = `
	INSERT INTO hour_lab_devices (device_uuid, last_seen_at, last_ip, app_version, current_session_id,
		open_apps, open_apps_at, ssh_public_key, diagnostic_note, hostname,
		cpu_percent, ram_percent, gpu_percent, gpu_name, metrics_at, gpu_power_watts)
	VALUES ($1, now(), $2, $3, $4::uuid, $5::jsonb, CASE WHEN $5::jsonb IS NULL THEN NULL ELSE now() END,
		NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''),
		$9, $10, $11, NULLIF($12, ''),
		CASE WHEN $13 THEN now() ELSE NULL END, $14)
	ON CONFLICT (device_uuid) DO UPDATE SET
		last_seen_at = now(), last_ip = $2, app_version = $3, current_session_id = $4::uuid,
		-- App antigo não manda a lista: manter a última conhecida é melhor que
		-- apagar e deixar a tela dizendo "nada aberto" numa máquina em uso.
		open_apps = CASE WHEN $5::jsonb IS NULL THEN hour_lab_devices.open_apps ELSE $5::jsonb END,
		open_apps_at = CASE WHEN $5::jsonb IS NULL THEN hour_lab_devices.open_apps_at ELSE now() END,
		-- Mandado uma vez só (primeiro heartbeat, ver autounattend.xml) — string
		-- vazia nos heartbeats seguintes não deve apagar a chave já registrada.
		ssh_public_key = CASE WHEN $6 = '' THEN hour_lab_devices.ssh_public_key ELSE $6 END,
		diagnostic_note = CASE WHEN $7 = '' THEN hour_lab_devices.diagnostic_note ELSE $7 END,
		hostname = CASE WHEN $8 = '' THEN hour_lab_devices.hostname ELSE $8 END,
		-- Métrica ausente mantém a última conhecida (mesma regra do resto): um
		-- PC sem GPU nunca manda gpu_percent, e um script antigo não manda
		-- nenhuma — em nenhum dos dois casos a coluna deve virar NULL de novo.
		cpu_percent = coalesce($9, hour_lab_devices.cpu_percent),
		ram_percent = coalesce($10, hour_lab_devices.ram_percent),
		gpu_percent = coalesce($11, hour_lab_devices.gpu_percent),
		gpu_name = CASE WHEN $12 = '' THEN hour_lab_devices.gpu_name ELSE $12 END,
		gpu_power_watts = coalesce($14, hour_lab_devices.gpu_power_watts),
		-- Só avança quando veio métrica de verdade, senão um PC que parou de
		-- reportar pareceria estar mandando uso novo a cada 60s.
		metrics_at = CASE WHEN $13 THEN now() ELSE hour_lab_devices.metrics_at END
	RETURNING name, unpair_requested, message_id::text, message_text,
		CASE WHEN pending_pair_token_expires_at > now() THEN pending_pair_token ELSE NULL END AS pending_pair_token,
		screenshot_requested_at IS NOT NULL AS screenshot_requested,
		lock_requested_at IS NOT NULL AS lock_requested, restart_requested_at IS NOT NULL AS restart_requested,
		shutdown_requested_at IS NOT NULL AS shutdown_requested,
		command_id::text, command_text`

// labDeviceHeartbeatClearSQL roda como comando SEPARADO, na mesma transação do
// upsert — é assim que a limpeza de fato acontece. Como o upsert já segurou o
// lock da linha, um pareamento concorrente (pairLabDeviceViaQR) fica bloqueado
// até o commit e só então grava o token novo: nenhum token se perde.
const labDeviceHeartbeatClearSQL = `
	UPDATE hour_lab_devices SET unpair_requested = false, pending_pair_token = NULL, pending_pair_token_expires_at = NULL,
		screenshot_delivered_at = CASE WHEN screenshot_requested_at IS NOT NULL THEN now() ELSE screenshot_delivered_at END,
		screenshot_requested_at = NULL,
		lock_requested_at = NULL, restart_requested_at = NULL, shutdown_requested_at = NULL
	WHERE device_uuid = $1
	  AND (unpair_requested = true OR pending_pair_token IS NOT NULL OR screenshot_requested_at IS NOT NULL
	       OR lock_requested_at IS NOT NULL OR restart_requested_at IS NOT NULL OR shutdown_requested_at IS NOT NULL)`

// labDeviceSecretLookupSQL trava a linha do dispositivo (FOR UPDATE) e lê o hash
// do segredo — a trava é o que serializa a adoção contra um heartbeat
// concorrente do mesmo device_uuid.
const labDeviceSecretLookupSQL = `
	SELECT device_secret_hash FROM hour_lab_devices WHERE device_uuid = $1 FOR UPDATE`

// labDeviceSecretMintSQL grava o hash do segredo recém-criado. O WHERE do
// DO UPDATE só deixa gravar sobre linha SEM segredo: se alguém já adotou o
// dispositivo, nada é devolvido e o chamador responde 401 em vez de sobrescrever
// o segredo de um PC já adotado.
const labDeviceSecretMintSQL = `
	INSERT INTO hour_lab_devices (device_uuid, device_secret_hash, device_secret_set_at)
	VALUES ($1, $2, now())
	ON CONFLICT (device_uuid) DO UPDATE SET
		device_secret_hash = EXCLUDED.device_secret_hash, device_secret_set_at = now()
	WHERE hour_lab_devices.device_secret_hash IS NULL OR hour_lab_devices.device_secret_hash = ''
	RETURNING device_secret_hash`

// labDeviceSecretBytes é o tamanho do segredo em bytes (vira o dobro em hex).
const labDeviceSecretBytes = 32

// migrateLabDeviceUUIDTx move um registro para uma identidade nova, preservando
// nome, sessão, inventário e histórico de capturas.
//
// Existe porque a identidade do PC morava só no config.json do app: perder esse
// arquivo numa reinstalação gerava um device_uuid novo, e o mesmo computador
// aparecia duas vezes no dashboard — aconteceu de verdade, com um registro
// virando fantasma e o outro tendo que ser renomeado na mão. A partir da 0.1.10
// o app deriva o id do MachineGuid do Windows (estável entre reinstalações e
// entre contas de usuário) e manda o id ANTIGO junto, uma vez, para o servidor
// costurar os dois.
//
// Registro que JÁ tem segredo só migra provando posse dele — sem isso, saber um
// device_uuid (que fica visível num QR na tela do PC) bastaria para sequestrar
// o registro de outra máquina. Registro sem segredo migra livre, pela mesma
// política de adoção aberta de authLabDeviceTx.
// Devolve resolvido=true quando não sobrou nada a migrar (migrou agora, ou o
// registro antigo já não existe). false significa "ainda pendente": o app
// continua mandando previousDeviceId nos próximos heartbeats, e a migração
// acontece sozinha assim que o impedimento sair do caminho — o admin esquece o
// segredo do registro antigo, ou apaga o registro duplicado.
func migrateLabDeviceUUIDTx(ctx context.Context, tx labDeviceQuerier, oldUUID, newUUID, deviceSecret string) (bool, error) {
	if oldUUID == "" || oldUUID == newUUID {
		return true, nil
	}
	var storedHash *string
	err := tx.QueryRow(ctx, labDeviceSecretLookupSQL, oldUUID).Scan(&storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil // registro antigo já não existe: nada a migrar
	}
	if err != nil {
		return false, err
	}
	// Registro SEM segredo é migrado sem prova, pela mesma política que
	// authLabDeviceTx usa pra adoção: exigir prova aqui não protegeria nada
	// (quem migrasse poderia adotar direto e ter o mesmo controle) e travaria
	// justamente o upgrade dos PCs em 0.1.0, que são anteriores ao segredo
	// existir e por isso não têm nenhum pra apresentar.
	if storedHash != nil && *storedHash != "" {
		if deviceSecret == "" ||
			subtle.ConstantTimeCompare([]byte(sha256Hex(deviceSecret)), []byte(*storedHash)) != 1 {
			return false, nil // segredo não confere: fica pendente, o app insiste
		}
	}
	// WHERE NOT EXISTS: se a identidade nova já tem registro próprio (o PC já
	// bateu heartbeat com ela), migrar apagaria esse — melhor deixar os dois e o
	// admin resolver do que perder dados.
	tag, err := tx.Exec(ctx, `
		UPDATE hour_lab_devices SET device_uuid = $2
		WHERE device_uuid = $1
		  AND NOT EXISTS (SELECT 1 FROM hour_lab_devices WHERE device_uuid = $2)`,
		oldUUID, newUUID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// upsertLabDeviceHeartbeat autentica o dispositivo, grava/atualiza o heartbeat e
// devolve os comandos pendentes, zerando unpair_requested/pending_pair_token na
// mesma transação — assim cada comando é entregue exatamente uma vez.
// message_id/text não são zerados: o app deduplica localmente pelo id.
func (s *Server) upsertLabDeviceHeartbeat(ctx context.Context, hb labHeartbeat) (*LabDeviceHeartbeatResult, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do Commit
	res, err := upsertLabDeviceHeartbeatTx(ctx, tx, hb)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	// Depois do commit, nunca antes: o estado ao vivo não pode anunciar um
	// heartbeat que a transação acabou de desfazer. Best-effort por design —
	// ver hour_lab_live_state.go.
	s.saveLabLiveState(ctx, hb.DeviceUUID, liveStateFrom(hb, time.Now().UTC()))
	return res, nil
}

// authLabDeviceTx resolve a credencial do dispositivo. Devolve o segredo em
// texto puro APENAS quando acabou de criá-lo (dispositivo novo, ou legado de
// antes desta coluna existir) — é a única vez que ele trafega; no banco só fica
// o sha256.
//
// A adoção de um device_uuid legado (sem segredo) é aberta de propósito:
// bloquear esses PCs brickaria o laboratório inteiro no deploy. A janela é de um
// heartbeat por PC (~30s) e fica visível pro admin em hasSecret na listagem; se
// um PC nunca adotar, é sinal de que alguém adotou antes dele — o admin resolve
// com resetLabDeviceSecret.
func authLabDeviceTx(ctx context.Context, tx labDeviceQuerier, deviceUUID, deviceSecret string) (*string, error) {
	var storedHash *string
	err := tx.QueryRow(ctx, labDeviceSecretLookupSQL, deviceUUID).Scan(&storedHash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		storedHash = nil // dispositivo novo: cai na adoção abaixo
	case err != nil:
		return nil, err
	}

	if storedHash != nil && *storedHash != "" {
		if deviceSecret == "" ||
			subtle.ConstantTimeCompare([]byte(sha256Hex(deviceSecret)), []byte(*storedHash)) != 1 {
			return nil, errLabDeviceUnauthorized
		}
		return nil, nil
	}

	secret := randomToken(labDeviceSecretBytes)
	var applied *string
	if err := tx.QueryRow(ctx, labDeviceSecretMintSQL, deviceUUID, sha256Hex(secret)).Scan(&applied); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Outra requisição adotou o dispositivo neste exato instante.
			return nil, errLabDeviceUnauthorized
		}
		return nil, err
	}
	return &secret, nil
}

func upsertLabDeviceHeartbeatTx(ctx context.Context, tx labDeviceQuerier, hb labHeartbeat) (*LabDeviceHeartbeatResult, error) {
	// Antes de autenticar: se o app trocou de identidade (0.1.10+), costura o
	// registro antigo na nova em vez de deixar o PC duplicado.
	migrado, err := migrateLabDeviceUUIDTx(ctx, tx, hb.PreviousUUID, hb.DeviceUUID, hb.DeviceSecret)
	if err != nil {
		return nil, err
	}
	minted, err := authLabDeviceTx(ctx, tx, hb.DeviceUUID, hb.DeviceSecret)
	if err != nil {
		return nil, err
	}

	// Lê antes de escrever. O que é volátil (visto por último, IP, métricas,
	// apps abertos) vai pro Redis a cada heartbeat; o Postgres só é tocado
	// quando muda algo DURÁVEL ou quando vence o piso de frescor. Antes disto
	// todo heartbeat fazia um UPDATE — 1440 por dia por PC, com 96% deles
	// reescrevendo índice à toa (medido em produção, ver hour_lab_live_state.go).
	var res LabDeviceHeartbeatResult
	var appVersionAtual, hostnameAtual, sshAtual, notaAtual, sessaoAtual string
	precisaEscrever := false
	err = tx.QueryRow(ctx, labDeviceHeartbeatReadSQL, hb.DeviceUUID, labLiveFloor.Seconds()).
		Scan(&res.Name, &res.UnpairRequested, &res.MessageID, &res.MessageText, &res.PairToken,
			&res.ScreenshotRequested, &res.LockRequested, &res.RestartRequested, &res.ShutdownRequested,
			&res.CommandID, &res.CommandText, &appVersionAtual, &hostnameAtual, &sshAtual, &notaAtual,
			&sessaoAtual, &precisaEscrever)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Linha ainda não existe: o upsert abaixo é quem a cria.
		precisaEscrever = true
	case err != nil:
		return nil, err
	default:
		// Campo "grava a última, vazio não apaga": só conta como mudança quando
		// veio valor E ele difere. Sem isso, um heartbeat sem chave SSH (o caso
		// normal) pareceria estar apagando a chave e forçaria escrita sempre.
		precisaEscrever = precisaEscrever ||
			hb.AppVersion != appVersionAtual ||
			(hb.Hostname != "" && hb.Hostname != hostnameAtual) ||
			(hb.SSHPublicKey != "" && hb.SSHPublicKey != sshAtual) ||
			(hb.DiagnosticNote != "" && hb.DiagnosticNote != notaAtual) ||
			sessionIDText(hb.SessionID) != sessaoAtual
	}

	if precisaEscrever {
		// O upsert é o MESMO de sempre, com a mesma semântica de "ausente
		// mantém o anterior" — só deixou de rodar a cada 60s.
		if err := tx.QueryRow(ctx, labDeviceHeartbeatUpsertSQL, hb.DeviceUUID, hb.IP, hb.AppVersion, hb.SessionID,
			hb.OpenApps, hb.SSHPublicKey, hb.DiagnosticNote, hb.Hostname,
			hb.CPUPercent, hb.RAMPercent, hb.GPUPercent, hb.GPUName, hb.HasMetrics(), hb.GPUPowerWatts).
			Scan(&res.Name, &res.UnpairRequested, &res.MessageID, &res.MessageText, &res.PairToken,
				&res.ScreenshotRequested, &res.LockRequested, &res.RestartRequested, &res.ShutdownRequested,
				&res.CommandID, &res.CommandText); err != nil {
			return nil, err
		}
	}
	res.PreviousDeviceResolved = migrado
	if res.UnpairRequested || res.PairToken != nil || res.ScreenshotRequested ||
		res.LockRequested || res.RestartRequested || res.ShutdownRequested {
		if _, err := tx.Exec(ctx, labDeviceHeartbeatClearSQL, hb.DeviceUUID); err != nil {
			return nil, err
		}
	}
	if minted != nil {
		res.DeviceSecret = minted
		// Quem acabou de adotar o dispositivo não apresentou credencial nenhuma:
		// o token de pareamento não sai nesta resposta (foi zerado acima; o
		// admin repareia). Vale só na adoção, e só neste heartbeat.
		res.PairToken = nil
	}
	return &res, nil
}

// resetLabDeviceSecret esquece o segredo do PC: o próximo heartbeat vira uma
// adoção nova. Escotilha de operação pra quando o PC perde o arquivo de config
// mas mantém o device_uuid — sem isto ele ficaria travado em 401 pra sempre.
// deleteLabDevice remove o registro do PC (entrada fantasma de teste, ou
// máquina que saiu de operação). Sessões que ele já rodou não são afetadas.
func (s *Server) deleteLabDevice(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM hour_lab_devices WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

func (s *Server) resetLabDeviceSecret(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_lab_devices
		SET device_secret_hash = NULL, device_secret_set_at = NULL, pending_pair_token = NULL
		WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

const labDeviceCols = `d.id::text, d.device_uuid, d.name, d.last_seen_at, d.last_ip, d.app_version,
	d.current_session_id::text, c.name, s.status,
	(d.device_secret_hash IS NOT NULL AND d.device_secret_hash != '') AS has_secret, d.created_at,
	coalesce(d.open_apps, '[]'::jsonb), d.open_apps_at, d.ssh_public_key, d.diagnostic_note,
	d.hostname, d.cpu_percent, d.ram_percent, d.gpu_percent, d.gpu_name, d.metrics_at,
	d.lock_requested_at, d.restart_requested_at, d.shutdown_requested_at,
	d.command_text, d.command_sent_at, d.command_result, d.command_result_at, d.gpu_power_watts`

func scanLabDevice(row pgx.Row) (*LabDevice, error) {
	var d LabDevice
	err := row.Scan(&d.ID, &d.DeviceUUID, &d.Name, &d.LastSeenAt, &d.LastIP, &d.AppVersion,
		&d.CurrentSessionID, &d.CurrentClientName, &d.CurrentStatus, &d.HasSecret, &d.CreatedAt,
		&d.OpenApps, &d.OpenAppsAt, &d.SSHPublicKey, &d.DiagnosticNote,
		&d.Hostname, &d.CPUPercent, &d.RAMPercent, &d.GPUPercent, &d.GPUName, &d.MetricsAt,
		&d.LockRequestedAt, &d.RestartRequestedAt, &d.ShutdownRequestedAt,
		&d.CommandText, &d.CommandSentAt, &d.CommandResult, &d.CommandResultAt, &d.GPUPowerWatts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// resolveOpenAppIcons preenche isto na listagem; começa {} pra o JSON de um
	// PC que não passou por lá (ou sem apps abertos) nunca sair null.
	d.OpenAppIcons = map[string]string{}
	return &d, nil
}

// Paginação da listagem de PCs: o heartbeat cria uma linha por device_uuid
// visto, e um device_uuid novo é aceito sem credencial (é assim que um PC se
// adota) — ou seja, a tabela cresce sem teto natural. Sem LIMIT, um dia a
// listagem do admin traria a tabela inteira numa resposta só.
const (
	labDevicesDefaultLimit = 200
	labDevicesMaxLimit     = 500
)

// listLabDevices traz uma página de PCs já vistos ao menos uma vez, com a sessão
// atual (se houver) — "online/offline" é decidido pelo front a partir de
// lastSeenAt (o backend não guarda esse estado, só o fato observado). Devolve
// também o total, pra o admin perceber quando há mais do que cabe na página.
// A ordenação casa com idx_hour_lab_devices_order.
func (s *Server) listLabDevices(ctx context.Context, limit, offset int) ([]LabDevice, int64, error) {
	var total int64
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM hour_lab_devices`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, `
		SELECT `+labDeviceCols+`
		FROM hour_lab_devices d
		LEFT JOIN hour_sessions s ON s.id = d.current_session_id
		LEFT JOIN hour_clients c ON c.id = s.client_id
		ORDER BY d.name NULLS LAST, d.device_uuid
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []LabDevice{}
	for rows.Next() {
		d, err := scanLabDevice(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Depois de fechar o cursor: resolveOpenAppIcons faz outra query no mesmo
	// pool, e com o cursor aberto ela precisaria de uma segunda conexão à toa.
	rows.Close()
	// O Redis tem o estado mais fresco (o Postgres só é escrito a cada
	// labLiveFloor); sobrepor ANTES de resolver os ícones, porque a lista de
	// apps abertos é justamente uma das coisas que vêm de lá.
	s.applyLabLiveState(ctx, out)
	if err := s.resolveOpenAppIcons(ctx, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (s *Server) renameLabDevice(ctx context.Context, id, name string) (*LabDevice, error) {
	tag, err := s.db.Exec(ctx, `UPDATE hour_lab_devices SET name = $2 WHERE id = $1::uuid`, id, name)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, errLabDeviceNotFound
	}
	d, err := scanLabDevice(s.db.QueryRow(ctx, `
		SELECT `+labDeviceCols+`
		FROM hour_lab_devices d
		LEFT JOIN hour_sessions s ON s.id = d.current_session_id
		LEFT JOIN hour_clients c ON c.id = s.client_id
		WHERE d.id = $1::uuid`, id))
	if err != nil || d == nil {
		return d, err
	}
	// Mesmo tratamento da listagem: o dashboard substitui o item da lista pelo
	// que volta daqui, e sem isto os ícones e o estado ao vivo sumiriam do card
	// até o próximo poll.
	one := []LabDevice{*d}
	s.applyLabLiveState(ctx, one)
	if err := s.resolveOpenAppIcons(ctx, one); err != nil {
		return nil, err
	}
	return &one[0], nil
}

// requestLabDeviceUnpair pede pro PC voltar sozinho pra tela de colar link no
// próximo heartbeat — não derruba a sessão em si (isso é feito por
// endHourSession/transitionHourSession), só solta o pareamento local do PC.
func (s *Server) requestLabDeviceUnpair(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE hour_lab_devices SET unpair_requested = true WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

// pairLabDeviceViaQR inicia uma sessão nova pro cliente escolhido (mesma
// lógica de startHourSession) e deixa o token pronto pra entrega no próximo
// heartbeat do PC — fluxo do QR: PC mostra um QR com o próprio device_uuid,
// admin escaneia com o celular (já logado) em /admin/horas/parear/:deviceUuid,
// escolhe o cliente, e o PC recebe o token sozinho, sem digitar nada.
func (s *Server) pairLabDeviceViaQR(ctx context.Context, deviceUUID, clientID string, createdBy int64) (*HourSession, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hour_lab_devices WHERE device_uuid = $1)`, deviceUUID).
		Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, errLabDeviceNotFound
	}
	// Checagem de existência acima, não uma transação com o INSERT/UPDATE
	// abaixo: janela de corrida (o PC some entre as duas queries) é
	// improvável e inofensiva — na pior hipótese a sessão fica sem o token
	// entregue, e o admin repete o pareamento.
	// Pareamento via QR não tem UI pra agendar início/fim — sessão sempre
	// começa na hora e de duração livre (mesmo comportamento de antes desses
	// campos existirem).
	h, token, _, err := s.startHourSession(ctx, clientID, createdBy, nil, nil)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, labDevicePairTokenSQL, deviceUUID, token, pairTokenTTL.Seconds()); err != nil {
		return nil, err
	}
	return h, nil
}

// sendLabDeviceMessage grava um aviso pro PC mostrar no próximo heartbeat.
// message_id novo a cada envio é o que permite mandar o mesmo texto de novo
// (o app só reexibe quando o id muda em relação ao último que já mostrou).
func (s *Server) sendLabDeviceMessage(ctx context.Context, id, text string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_lab_devices
		SET message_id = gen_random_uuid(), message_text = $2, message_sent_at = now()
		WHERE id = $1::uuid`,
		id, text)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

// maxCommandTextLength/maxCommandResultLength: comando livre digitado pelo
// admin e a saída que o PC devolve — tetos generosos (é PowerShell rodado por
// quem já tem acesso admin ao dashboard, não input de terceiro), só pra não
// deixar a coluna crescer sem limite com um script colado inteiro.
const (
	maxCommandTextLength   = 4000
	maxCommandResultLength = 8000
)

// requestLabDeviceLock/Restart/Shutdown: mesma semântica de unpair_requested
// — regravar por cima de um pedido pendente é inofensivo (continua sendo a
// mesma ação), então não há erro de "já pediu". Entregues exatamente uma vez
// pelo heartbeat (ver labDeviceHeartbeatClearSQL).
func (s *Server) requestLabDeviceLock(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE hour_lab_devices SET lock_requested_at = now() WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

func (s *Server) requestLabDeviceRestart(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE hour_lab_devices SET restart_requested_at = now() WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

func (s *Server) requestLabDeviceShutdown(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE hour_lab_devices SET shutdown_requested_at = now() WHERE id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

// sendLabDeviceCommand grava um comando PowerShell livre pro PC rodar no
// próximo heartbeat. command_id novo a cada envio (mesma ideia do message_id)
// é o que permite mandar o MESMO texto de novo e o app reconhecer como um
// pedido novo. Zera o resultado anterior: um resultado velho ao lado de um
// comando novo confundiria mais do que ajudaria.
func (s *Server) sendLabDeviceCommand(ctx context.Context, id, text string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE hour_lab_devices
		SET command_id = gen_random_uuid(), command_text = $2, command_sent_at = now(),
			command_result = NULL, command_result_at = NULL
		WHERE id = $1::uuid`,
		id, text)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errLabDeviceNotFound
	}
	return nil
}

// storeLabDeviceCommandResult grava o resultado que o PC reportou. Autentica
// pelo segredo do dispositivo (rota pública, mesmo padrão do heartbeat) e só
// aceita quando commandID bate com o comando pendente atual — um resultado
// atrasado de um comando JÁ SUBSTITUÍDO por um mais novo não deve sobrescrever
// o resultado do comando atual.
func (s *Server) storeLabDeviceCommandResult(ctx context.Context, deviceUUID, deviceSecret, commandID, result string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op depois do Commit

	minted, err := authLabDeviceTx(ctx, tx, deviceUUID, deviceSecret)
	if err != nil {
		return err
	}
	if minted != nil {
		return errLabDeviceUnauthorized
	}

	tag, err := tx.Exec(ctx, `
		UPDATE hour_lab_devices
		SET command_result = $3, command_result_at = now()
		WHERE device_uuid = $1 AND command_id::text = $2`,
		deviceUUID, commandID, result)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Não é erro: comando já foi substituído por outro, ou device_uuid não
		// existe (heartbeat nunca chegou a criar a linha) — o PC não tem como
		// distinguir os dois casos, e nenhum dos dois precisa de retry dele.
		return tx.Commit(ctx)
	}
	return tx.Commit(ctx)
}

// Tetos da lista de apps abertos: o PC manda a cada heartbeat (2/min), então o
// payload precisa de limite. Uma máquina em uso tem 10-30 janelas; 100 cobre
// qualquer cenário real e barra quem tente inflar a coluna.
const (
	maxOpenApps          = 100
	maxOpenAppNameLength = 100
)

// encodeOpenApps prepara a lista para a coluna jsonb. Devolve nil quando o app
// não mandou nada (versão antiga) — o SQL do heartbeat trata nil como "mantém a
// última lista conhecida", em vez de apagar e dizer "nada aberto" numa máquina
// que está em uso.
func encodeOpenApps(apps []string) []byte {
	if apps == nil {
		return nil
	}
	clean := make([]string, 0, len(apps))
	for _, a := range apps {
		name := cleanOpenAppName(a)
		if name == "" {
			continue
		}
		clean = append(clean, name)
		if len(clean) >= maxOpenApps {
			break
		}
	}
	out, err := json.Marshal(clean)
	if err != nil {
		return nil
	}
	return out
}

// cleanOpenAppName é a normalização de um nome de app aberto — a mesma pra
// lista e pras chaves do mapa de ícones, senão as duas não batem.
func cleanOpenAppName(raw string) string {
	name := appDisplayName(sanitizeInventoryField(raw))
	if len([]rune(name)) > maxOpenAppNameLength {
		name = truncRunes(name, maxOpenAppNameLength)
	}
	return name
}

// appDisplayName resolve o nome que vai pra tela. Programa comum devolve o nome
// ("Zen", "NVIDIA App"), mas app empacotado da Microsoft Store devolve o
// CAMINHO do executável — e a lista mostrava
// "C:\Program Files\WindowsApps\Microsoft.ScreenSketch_11.26...\SnippingTool\Sni",
// cortado no meio pelo teto de tamanho. O app 0.1.10+ já manda resolvido; isto
// cobre os PCs que ainda estão na 0.1.9.
func appDisplayName(s string) string {
	if !strings.ContainsAny(s, `\/`) {
		return s
	}
	base := s
	if i := strings.LastIndexAny(base, `\/`); i >= 0 {
		base = base[i+1:]
	}
	base = strings.TrimSpace(base)
	if ext := strings.ToLower(base); strings.HasSuffix(ext, ".exe") {
		base = base[:len(base)-4]
	}
	return base
}
