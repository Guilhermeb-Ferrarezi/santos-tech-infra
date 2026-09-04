package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/jackc/pgx/v5"
)

// Shell remoto interativo pra um PC de laboratório: o AGENTE (script no PC,
// serviço separado do watchdog de 60s) mantém uma conexão WebSocket sempre
// aberta com o backend, esperando parado. Quando um admin abre a tela de
// shell de um PC, o backend acha a conexão do agente daquele device na hub e
// vira um RELAY puro entre as duas pontas — não interpreta bytes de
// stdin/stdout, só passa adiante. A única coisa que o backend injeta são dois
// controles em JSON (start/stop), pra o agente saber quando nascer/matar o
// processo powershell.exe real.
//
// Por que não dá pra usar o mecanismo de heartbeat/comando já existente: é
// "dispara e espera até ~60s pelo próximo heartbeat" — inaceitável pra
// terminal interativo (cada tecla precisa aparecer na hora). Daí a conexão
// persistente, em vez de poll.

// shellAgent é a conexão de um PC esperando por uma sessão. `busy` impede
// dois admins tomarem a mesma sessão ao mesmo tempo — o segundo recebe erro
// em vez de os dois brigarem pelo mesmo processo powershell.exe.
type shellAgent struct {
	conn *websocket.Conn
	mu   sync.Mutex
	busy bool
}

// shellHub indexa os agentes conectados por device_uuid (o identificador que
// o PRÓPRIO PC conhece de si mesmo — o mesmo usado no heartbeat). A rota do
// admin resolve {id} (a PK da listagem) pra device_uuid antes de consultar
// aqui, porque o agente não tem como saber sua própria PK do banco.
type shellHub struct {
	mu     sync.Mutex
	agents map[string]*shellAgent
}

func newShellHub() *shellHub { return &shellHub{agents: map[string]*shellAgent{}} }

func (h *shellHub) register(deviceUUID string, a *shellAgent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agents[deviceUUID] = a
}

// unregister só remove se for AINDA a mesma conexão — evita que uma conexão
// velha (fechando com atraso) apague o registro de uma reconexão mais nova.
func (h *shellHub) unregister(deviceUUID string, a *shellAgent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agents[deviceUUID] == a {
		delete(h.agents, deviceUUID)
	}
}

func (h *shellHub) get(deviceUUID string) *shellAgent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agents[deviceUUID]
}

// wsOriginOpts monta as opções de Accept com a MESMA checagem de Origin do
// resto do ecossistema (ver agent-go/ws.go) — sem isso, a autenticação por
// cookie do WS fica vulnerável a CSWSH (qualquer site abre o WS usando a
// sessão do admin logado).
func (s *Server) wsOriginOpts() *websocket.AcceptOptions {
	opts := &websocket.AcceptOptions{OriginPatterns: s.cfg.CORSOrigins}
	if len(s.cfg.CORSOrigins) == 0 && !s.cfg.Production {
		opts.InsecureSkipVerify = true // só em dev
	}
	return opts
}

// lookupDeviceUUID traduz a PK da listagem (usada em toda rota /hour-lab-devices/{id})
// pro device_uuid que o agente conhece de si mesmo.
func (s *Server) lookupDeviceUUID(ctx context.Context, id string) (string, error) {
	var deviceUUID string
	err := s.db.QueryRow(ctx, `SELECT device_uuid FROM hour_lab_devices WHERE id = $1::uuid`, id).Scan(&deviceUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errLabDeviceNotFound
	}
	return deviceUUID, err
}

// verifyLabDeviceShellSecret autentica o AGENTE (não o admin) — mesmo segredo
// de dispositivo que o heartbeat já usa (ver authLabDeviceTx), mas em modo
// estritamente verificação: sem a adoção automática de dispositivo novo, que
// só faz sentido no primeiro heartbeat. Um PC sem segredo ainda adotado não
// consegue abrir o agente de shell — tem que mandar heartbeat primeiro.
func (s *Server) verifyLabDeviceShellSecret(ctx context.Context, deviceUUID, deviceSecret string) error {
	var storedHash *string
	err := s.db.QueryRow(ctx, `SELECT device_secret_hash FROM hour_lab_devices WHERE device_uuid = $1`, deviceUUID).Scan(&storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return errLabDeviceUnauthorized
	}
	if err != nil {
		return err
	}
	if storedHash == nil || *storedHash == "" || deviceSecret == "" ||
		subtle.ConstantTimeCompare([]byte(sha256Hex(deviceSecret)), []byte(*storedHash)) != 1 {
		return errLabDeviceUnauthorized
	}
	return nil
}

// shellAgentAuth é a primeira mensagem que o agente manda ao conectar.
type shellAgentAuth struct {
	DeviceID     string `json:"deviceId"`
	DeviceSecret string `json:"deviceSecret"`
}

// shellControl é o único tipo de mensagem de TEXTO que o backend injeta no
// relay (start/stop) — tudo mais trafega como frame BINÁRIO puro (stdin de
// um lado, stdout/stderr do outro), sem o backend olhar dentro.
type shellControl struct {
	Type string `json:"type"` // start|stop
}

// GET /public/lab-devices/shell-agent — o AGENTE (script no PC) conecta aqui
// e fica parado esperando. Público (auth é o segredo de dispositivo na
// primeira mensagem, mesmo padrão do heartbeat), mas exige o segredo já
// adotado — não faz sentido um PC nunca visto abrir sessão de shell.
func (s *Server) handleLabDeviceShellAgent(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, s.wsOriginOpts())
	if err != nil {
		return
	}
	defer c.CloseNow()

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	var auth shellAgentAuth
	err = wsjson.Read(ctx, c, &auth)
	cancel()
	if err != nil {
		c.Close(websocket.StatusPolicyViolation, "auth esperada como primeira mensagem")
		return
	}
	if authErr := s.verifyLabDeviceShellSecret(r.Context(), auth.DeviceID, auth.DeviceSecret); authErr != nil {
		c.Close(websocket.StatusPolicyViolation, "credencial inválida")
		return
	}

	agent := &shellAgent{conn: c}
	s.labShellHub.register(auth.DeviceID, agent)
	defer s.labShellHub.unregister(auth.DeviceID, agent)

	// Não há mais nada a LER daqui pra fora até um admin anexar — o próprio
	// relay (handleLabDeviceShellWS) passa a ler/escrever nesta mesma conn
	// quando chega a hora. Este handler só precisa ficar vivo (senão o HTTP
	// encerra a conexão) e perceber quando o agente cai (ping/pong do
	// protocolo WS cobre isso via CloseRead).
	ctx2 := c.CloseRead(r.Context())
	<-ctx2.Done()
}

// GET /hour-lab-devices/{id}/shell-check — checagem REST antes de abrir o WS.
// Existe só por uma limitação do navegador: um handshake de WebSocket que
// falha não deixa o JS ler o status HTTP nem o corpo da resposta (por
// design, contra vazamento de info entre origens) — então o 403
// SUDO_REQUIRED que o sudoGuard devolveria não dá pra distinguir de
// "servidor fora do ar" no lado do cliente, e o account-kit não tem como
// interceptar e mandar pro /confirm. Uma chamada REST comum ANTES do WS
// (mesmo sudoGuard, mesma janela de elevação) resolve: se isto passar, o WS
// também vai passar. De quebra, já informa se o agente do PC está
// conectado — sem isso, uma tentativa de WS contra um PC sem agente falharia
// do mesmo jeito silencioso (erro genérico, sem "PC não está com o agente
// rodando" pra mostrar).
func (s *Server) handleLabDeviceShellCheck(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	deviceUUID, err := s.lookupDeviceUUID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	agentOnline := s.labShellHub.get(deviceUUID) != nil
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"agentOnline": agentOnline})
}

// GET /hour-lab-devices/{id}/shell — o ADMIN conecta aqui pra abrir uma
// sessão. Faz o relay bruto entre esta conexão e a do agente já registrado
// na hub. adminGuard+sudoGuard na rota (ver routes.go) — shell interativo é
// bem mais sensível que os comandos "dispara e esquece" já existentes
// (restart/shutdown), que hoje só são admin-only.
func (s *Server) handleLabDeviceShellWS(w http.ResponseWriter, r *http.Request) {
	id, err := hourUUIDFrom(r, "id", errLabDeviceNotFound)
	if err != nil {
		writeErr(w, err)
		return
	}
	deviceUUID, err := s.lookupDeviceUUID(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	agent := s.labShellHub.get(deviceUUID)
	if agent == nil {
		writeErr(w, appErr(http.StatusServiceUnavailable, "SHELL_AGENT_OFFLINE", "PC sem o agente de shell conectado agora"))
		return
	}
	agent.mu.Lock()
	if agent.busy {
		agent.mu.Unlock()
		writeErr(w, appErr(http.StatusConflict, "SHELL_BUSY", "Já tem uma sessão de shell aberta com este PC"))
		return
	}
	agent.busy = true
	agent.mu.Unlock()
	defer func() {
		agent.mu.Lock()
		agent.busy = false
		agent.mu.Unlock()
	}()

	adminConn, err := websocket.Accept(w, r, s.wsOriginOpts())
	if err != nil {
		return
	}
	defer adminConn.CloseNow()

	startCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	startErr := wsjson.Write(startCtx, agent.conn, shellControl{Type: "start"})
	cancel()
	if startErr != nil {
		adminConn.Close(websocket.StatusInternalError, "falha ao iniciar sessão no agente")
		return
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = wsjson.Write(stopCtx, agent.conn, shellControl{Type: "stop"})
		stopCancel()
	}()

	ctx, cancelRelay := context.WithCancel(r.Context())
	defer cancelRelay()

	// Dois goroutines, um por direção — cada Conn só é LIDA por uma goroutine
	// (coder/websocket não permite leitura concorrente na mesma conn); Write
	// é seguro mesmo se algo mais escrever na mesma conn em paralelo (aqui
	// não escreve, mas vale registrar a garantia usada).
	go func() {
		defer cancelRelay()
		relayBinary(ctx, adminConn, agent.conn)
	}()
	relayBinary(ctx, agent.conn, adminConn)
}

// relayBinary lê frames binários de `from` e escreve em `to` até o contexto
// cancelar ou a leitura falhar (conexão fechada de qualquer lado). Frames de
// texto (controle) do agente, se vierem nesta direção, são ignorados aqui —
// só start/stop interessam, e esses são injetados fora do relay.
func relayBinary(ctx context.Context, from, to *websocket.Conn) {
	for {
		typ, data, err := from.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}
		if err := to.Write(ctx, websocket.MessageBinary, data); err != nil {
			return
		}
	}
}
