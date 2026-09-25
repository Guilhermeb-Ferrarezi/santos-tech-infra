package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// PlaybookFonte — as fichas ativas de cada tenant, lidas a cada mensagem.
//
// Mesmo desenho da RegrasVendaFonte: cache curto (30 s) invalidado quando o
// painel muda uma ficha, e NUNCA falha — banco fora do ar devolve a última
// lista lida, e sem nenhuma, lista vazia (o bot segue com os princípios).
type PlaybookFonte struct {
	leitor interface {
		Ativas(ctx context.Context, tenant TenantID) ([]Situacao, error)
	}
	// uso grava em que conversa cada ficha foi usada. Pode ser nil (testes).
	uso interface {
		RegistraUso(ctx context.Context, tenant TenantID, contato, conversa string, ids []string) error
	}
	ttl    time.Duration
	logger *slog.Logger
	agora  func() time.Time

	mu    sync.Mutex
	cache map[TenantID]fichasEmCache
}

type fichasEmCache struct {
	fichas  []Situacao
	valeAte time.Time
}

func NewPlaybookFonte(leitor interface {
	Ativas(ctx context.Context, tenant TenantID) ([]Situacao, error)
}, ttl time.Duration, logger *slog.Logger) *PlaybookFonte {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	f := &PlaybookFonte{leitor: leitor, ttl: ttl, logger: logger, agora: time.Now, cache: map[TenantID]fichasEmCache{}}
	if u, ok := leitor.(interface {
		RegistraUso(ctx context.Context, tenant TenantID, contato, conversa string, ids []string) error
	}); ok {
		f.uso = u
	}
	return f
}

// Ativas devolve as fichas ativas do tenant.
func (f *PlaybookFonte) Ativas(ctx context.Context, tenant TenantID) []Situacao {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	c, tem := f.cache[tenant]
	f.mu.Unlock()
	if tem && f.agora().Before(c.valeAte) {
		return c.fichas
	}
	fichas, err := f.leitor.Ativas(ctx, tenant)
	if err != nil {
		f.logger.Error("playbook: leitura das fichas falhou; usando a última lista lida", "err", err)
		if tem {
			return c.fichas
		}
		return []Situacao{}
	}
	f.mu.Lock()
	f.cache[tenant] = fichasEmCache{fichas: fichas, valeAte: f.agora().Add(f.ttl)}
	f.mu.Unlock()
	return fichas
}

// Invalida força a próxima leitura a ir ao banco (a lista atual fica de
// reserva caso ele falhe).
func (f *PlaybookFonte) Invalida(tenant TenantID) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if c, ok := f.cache[tenant]; ok {
		c.valeAte = time.Time{}
		f.cache[tenant] = c
	}
	f.mu.Unlock()
}

// RegistraUso grava o uso; falha só loga — medir não pode derrubar resposta.
func (f *PlaybookFonte) RegistraUso(ctx context.Context, tenant TenantID, contato, conversa string, ids []string) {
	if f == nil || f.uso == nil || len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := f.uso.RegistraUso(ctx, tenant, contato, conversa, ids); err != nil {
		f.logger.Error("playbook: registrar uso das fichas", "err", err, "conversa", conversa)
	}
}
