package main

import (
	"context"
	"fmt"
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
	if err := ls.start(ctx); err != nil {
		return nil, err
	}

	// insere só após start OK; re-check de corrida (outra goroutine pode ter criado a sessão)
	m.mu.Lock()
	if existing := m.live[conv.ID]; existing != nil {
		m.mu.Unlock()
		killProcessGroup(ls.cmd) // descarta o duplicado
		return existing, nil
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
