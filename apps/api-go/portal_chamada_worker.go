package main

import (
	"context"
	"log/slog"
	"time"
)

// Rotina que materializa as aulas sozinha, DENTRO do api-go.
//
// A primeira versão disso passava pelo cron-go, que chama api.santos-tech.com
// por HTTP com um Bearer de conta de serviço. Era indireção desnecessária: o
// api-go é dono da tabela e da função — não precisa se autenticar contra si
// mesmo, nem depender de uma credencial existir pra uma tarefa interna. O
// cron-go continua fazendo sentido pra orquestrar OUTRO serviço, ou pra job
// que o admin configura na tela; não pra isto.

const (
	chamadaIntervalo  = 6 * time.Hour
	chamadaAtrasoBoot = 2 * time.Minute // deixa o boot terminar antes de varrer
	chamadaLockTTL    = 30 * time.Minute
	chamadaLockKey    = "api-go:chamada:gerar-aulas"
)

// startChamadaWorker dispara a rotina em background. Não bloqueia o boot e
// morre junto com o ctx do servidor.
func (s *Server) startChamadaWorker(ctx context.Context) {
	if s.portalDB == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(chamadaAtrasoBoot):
		}
		s.gerarAulasComLock(ctx)

		t := time.NewTicker(chamadaIntervalo)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.gerarAulasComLock(ctx)
			}
		}
	}()
}

// gerarAulasComLock roda a geração no máximo uma vez por janela em todo o
// cluster. Sem a trava, cada réplica geraria em paralelo — não corromperia
// nada (o INSERT é idempotente), mas seria trabalho repetido no banco a cada
// tique de toda instância.
func (s *Server) gerarAulasComLock(ctx context.Context) {
	if s.rdb != nil {
		ok, err := s.rdb.SetNX(ctx, chamadaLockKey, "1", chamadaLockTTL).Result()
		if err != nil {
			// Redis fora do ar não pode impedir a chamada de existir: segue
			// sem trava (o INSERT é idempotente, o pior caso é trabalho dobrado).
			slog.Warn("chamada: trava indisponível, gerando mesmo assim", "err", err)
		} else if !ok {
			return // outra instância já está cuidando desta janela
		}
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	turmas, criadas, err := s.portalGerarAulasDeTodasAsTurmas(c, 7)
	if err != nil {
		slog.Error("chamada: falha ao gerar aulas", "err", err)
		return
	}
	if criadas > 0 {
		slog.Info("chamada: aulas geradas", "turmas", turmas, "criadas", criadas)
	}
}
