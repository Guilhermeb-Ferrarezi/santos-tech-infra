package main

import (
	"context"
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
// Se o pool está cheio, hiberna a sessão ociosa menos recentemente usada (LRU).
func (m *SessionManager) ensureLive(ctx context.Context, conv *Conversation) (*liveSession, error) {
	m.mu.Lock()
	if ls := m.live[conv.ID]; ls != nil {
		m.mu.Unlock()
		return ls, nil
	}
	if len(m.live) >= m.maxLive() {
		if victim := m.lruIdleLocked(); victim != "" {
			ls := m.live[victim]
			delete(m.live, victim)
			m.mu.Unlock()
			killProcessGroup(ls.cmd) // hiberna; ressuscita via --resume na próxima msg
			m.mu.Lock()
		}
	}
	ls := m.newLiveSession(conv)
	// hooks de teste: quando o ClaudeBin é o próprio binário de teste, fala com o fake.
	if m.s.cfg.ClaudeBin == os.Args[0] {
		ls.testArgs = []string{"-test.run=TestHelperProcess"}
		ls.testEnv = append(os.Environ(), "GO_WANT_FAKE_CLAUDE=1")
	}
	m.live[conv.ID] = ls
	m.mu.Unlock()

	if err := ls.start(ctx); err != nil {
		m.mu.Lock()
		delete(m.live, conv.ID)
		m.mu.Unlock()
		return nil, err
	}
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
