package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// RegrasVendaFonte — de onde o engine tira as regras de venda a cada mensagem.
//
// Cache em memória com validade curta (30 s): as regras entram em todo prompt,
// e uma consulta ao banco por mensagem é desperdício para um texto que muda
// poucas vezes por semana. O PATCH da tela chama Invalida, então quem salvou
// vê a mudança já na próxima mensagem.
//
// NUNCA falha: banco fora do ar devolve a última versão lida; sem nenhuma
// lida, RegrasVenda{} — que resolve para o padrão do código. Um bot sem regra
// de venda é pior que um bot com a regra de ontem.
type RegrasVendaFonte struct {
	leitor interface {
		Atual(ctx context.Context, tenant TenantID) (VersaoRegras, error)
	}
	ttl    time.Duration
	logger *slog.Logger
	agora  func() time.Time

	mu    sync.Mutex
	cache map[TenantID]regrasEmCache
}

type regrasEmCache struct {
	regras  RegrasVenda
	valeAte time.Time // zero = vencido (Invalida), mas ainda serve de reserva
}

func NewRegrasVendaFonte(leitor interface {
	Atual(ctx context.Context, tenant TenantID) (VersaoRegras, error)
}, ttl time.Duration, logger *slog.Logger) *RegrasVendaFonte {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RegrasVendaFonte{leitor: leitor, ttl: ttl, logger: logger, agora: time.Now, cache: map[TenantID]regrasEmCache{}}
}

// Regras devolve as regras do tenant como estão salvas (sem resolver o
// padrão: quem monta o prompt resolve).
func (f *RegrasVendaFonte) Regras(ctx context.Context, tenant TenantID) RegrasVenda {
	if f == nil {
		return RegrasVenda{}
	}
	f.mu.Lock()
	c, tem := f.cache[tenant]
	f.mu.Unlock()
	if tem && f.agora().Before(c.valeAte) {
		return c.regras
	}

	v, err := f.leitor.Atual(ctx, tenant)
	if err != nil {
		f.logger.Error("regras de venda: leitura falhou; usando a última versão lida (ou o padrão)", "err", err)
		if tem {
			return c.regras
		}
		return RegrasVenda{}
	}
	f.mu.Lock()
	f.cache[tenant] = regrasEmCache{regras: v.Regras, valeAte: f.agora().Add(f.ttl)}
	f.mu.Unlock()
	return v.Regras
}

// Invalida força a próxima leitura a ir ao banco, mantendo o valor atual
// como reserva caso o banco falhe.
func (f *RegrasVendaFonte) Invalida(tenant TenantID) {
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
