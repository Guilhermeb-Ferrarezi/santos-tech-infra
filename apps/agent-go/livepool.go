package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"
)

func (m *SessionManager) maxLive() int {
	if m.s.cfg.MaxLive > 0 {
		return m.s.cfg.MaxLive
	}
	return 4
}

// ensureLive devolve a sessão viva da conversa, criando-a (spawn) se necessário.
// Cap é aplicado hibernando a sessão ociosa LRU para abrir vaga; se nenhuma estiver
// ociosa, retorna erro em vez de ultrapassar o limite (cap rígido).
// O spawn ocorre FORA do lock; a sessão só é inserida no mapa após start() ter êxito,
// com re-checagem de corrida para descartar duplicatas.
func (m *SessionManager) ensureLive(ctx context.Context, conv *Conversation) (*liveSession, error) {
	m.mu.Lock()
	if ls := m.live[conv.ID]; ls != nil {
		m.mu.Unlock()
		return ls, nil
	}
	// cap: hiberna a LRU ociosa para abrir vaga; se não há ociosa, recusa (cap rígido).
	if len(m.live) >= m.maxLive() {
		victim := m.lruIdleLocked()
		if victim == "" {
			m.mu.Unlock()
			return nil, fmt.Errorf("pool de sessões vivas cheio (%d/%d) e nenhuma ociosa para hibernar", len(m.live), m.maxLive())
		}
		ev := m.live[victim]
		delete(m.live, victim)
		m.mu.Unlock()
		killProcessGroup(ev.cmd) // hiberna; ressuscita via --resume na próxima msg
	} else {
		m.mu.Unlock()
	}

	// spawn FORA do lock
	ls := m.newLiveSession(conv)
	// hooks de teste: quando o ClaudeBin é o próprio binário de teste, fala com o fake.
	if m.s.cfg.ClaudeBin == os.Args[0] {
		ls.testArgs = []string{"-test.run=TestHelperProcess"}
		ls.testEnv = append(os.Environ(), "GO_WANT_FAKE_CLAUDE=1")
	}
	// O processo sobrevive à desconexão do cliente e é terminado por close()/killProcessGroup
	// (stdin fechado → EOF → readLoop encerra). NÃO passamos o ctx do caller (WS): quando o
	// cliente desconecta, o ctx é cancelado e as chamadas de DB/Redis dentro do readLoop
	// (insertMessage, markSessionStarted, etc.) falhariam silenciosamente, perdendo persistência.
	_ = ctx
	if err := ls.start(context.Background()); err != nil {
		return nil, err
	}

	// insere só após start OK; re-check de corrida (outra goroutine pode ter criado a sessão
	// ou preenchido o pool durante o spawn — ambos exigem descartar a sessão recém-nascida).
	m.mu.Lock()
	if existing := m.live[conv.ID]; existing != nil {
		m.mu.Unlock()
		killProcessGroup(ls.cmd) // descarta o duplicado
		return existing, nil
	}
	// re-valida cap: duas goroutines podem ter obtido vaga simultaneamente (evicção concorrente);
	// a que chegar depois encontra o pool cheio e deve descartar a sessão que acabou de spawnar.
	if len(m.live) >= m.maxLive() {
		m.mu.Unlock()
		killProcessGroup(ls.cmd)
		return nil, fmt.Errorf("pool de sessões vivas cheio (%d/%d) após spawn concorrente", len(m.live), m.maxLive())
	}
	m.live[conv.ID] = ls
	m.mu.Unlock()
	return ls, nil
}

// lruIdleLocked devolve o convID da sessão idle menos recentemente usada (mu já travado).
// Retorna "" se nenhuma está idle.
func (m *SessionManager) lruIdleLocked() string {
	var oldest string
	var oldestT time.Time
	for id, ls := range m.live {
		ls.mu.Lock()
		idle := ls.state == StatusIdle
		used := ls.lastUsed
		ls.mu.Unlock()
		if !idle {
			continue
		}
		if oldest == "" || used.Before(oldestT) {
			oldest, oldestT = id, used
		}
	}
	return oldest
}

// reapIdle hiberna sessões idle ociosas há mais que ttl e sem ninguém conectado (WS).
// Coleta as vítimas sob o lock, depois as fecha fora do lock para evitar I/O travado.
// Nota: hasSubs adquire m.mu, então verificamos m.subs diretamente (já dentro do lock).
func (m *SessionManager) reapIdle(ttl time.Duration) {
	now := time.Now()
	var victims []*liveSession
	m.mu.Lock()
	for id, ls := range m.live {
		ls.mu.Lock()
		idle := ls.state == StatusIdle && now.Sub(ls.lastUsed) > ttl
		ls.mu.Unlock()
		if idle && len(m.subs[id]) == 0 {
			victims = append(victims, ls)
			delete(m.live, id)
		}
	}
	m.mu.Unlock()
	for _, ls := range victims {
		slog.Info("hibernando sessão viva ociosa", "conv", ls.conv.ID)
		ls.close()
	}
}

// StartReaper roda reapIdle periodicamente até o ctx ser cancelado.
func (m *SessionManager) StartReaper(ctx context.Context) {
	ttl := 15 * time.Minute
	if m.s.cfg.IdleTTL > 0 {
		ttl = m.s.cfg.IdleTTL
	}
	go func() {
		t := time.NewTicker(ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.reapIdle(ttl)
			}
		}
	}()
}

// ShutdownLive fecha todas as sessões vivas (graceful) no desligamento do servidor.
func (m *SessionManager) ShutdownLive() {
	m.mu.Lock()
	all := make([]*liveSession, 0, len(m.live))
	for id, ls := range m.live {
		all = append(all, ls)
		delete(m.live, id)
	}
	m.mu.Unlock()
	for _, ls := range all {
		ls.close()
	}
}

// removeLive remove esta sessão do pool, mas SÓ se o mapa ainda apontar para ESTA
// instância — uma sessão mais nova (ressuscitada após hibernação/evicção) pode já ter
// tomado o lugar dela. Chamado pelo readLoop quando o processo morre.
func (m *SessionManager) removeLive(convID string, ls *liveSession) {
	m.mu.Lock()
	if m.live[convID] == ls {
		delete(m.live, convID)
	}
	m.mu.Unlock()
}

// Evict mata e remove a sessão viva de uma conversa (usado por /clear, /compact, /model
// — que mudam parâmetros de boot, exigindo reinício). A próxima mensagem ressuscita.
func (m *SessionManager) Evict(convID string) {
	m.mu.Lock()
	ls := m.live[convID]
	delete(m.live, convID)
	m.mu.Unlock()
	if ls != nil {
		signalProcessGroup(ls.cmd, syscall.SIGTERM)
	}
}
