package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Agendador: publica sozinho o post cujo horário chegou.
//
// Roda dentro do api-go, sem HTTP e sem credencial — o serviço já é dono dos
// posts e do publicador. Reaproveita exatamente o mesmo caminho do botão
// "publicar" (preparePublish + runPublish), pra publicação automática e manual
// não divergirem de comportamento.

const (
	agendadorIntervalo = time.Minute // pontualidade importa num post agendado
	agendadorJanela    = 7 * 24 * time.Hour
	agendadorLote      = 10
)

// agendadorUserID é o autor registrado nas notas e confirmações do que sai
// automaticamente. 0 = sistema; distingue no histórico o que foi publicado por
// gente do que saiu sozinho.
const agendadorUserID int64 = 0

func (s *Server) startAgendadorSocial(ctx context.Context) {
	if s.db == nil {
		return
	}
	go func() {
		t := time.NewTicker(agendadorIntervalo)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.publicarAgendadosVencidos(ctx)
			}
		}
	}()
}

// publicarAgendadosVencidos busca os posts com horário vencido e publica.
// Janela de 7 dias: post agendado pra muito tempo atrás não deve "acordar" e
// ir ao ar de surpresa se alguém religar o serviço meses depois.
func (s *Server) publicarAgendadosVencidos(ctx context.Context) {
	rows, err := s.db.Query(ctx,
		`SELECT id::text FROM social_posts
		 WHERE status = 'agendado'
		   AND auto_published_at IS NULL
		   AND scheduled_at IS NOT NULL
		   AND scheduled_at <= NOW()
		   AND scheduled_at > NOW() - $1::interval
		 ORDER BY scheduled_at
		 LIMIT $2`,
		fmt.Sprintf("%d hours", int(agendadorJanela.Hours())), agendadorLote)
	if err != nil {
		slog.Error("agendador social: falha ao buscar posts vencidos", "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			slog.Error("agendador social: falha ao ler post", "err", err)
			return
		}
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		s.publicarAgendado(ctx, id)
	}
}

func (s *Server) publicarAgendado(ctx context.Context, id string) {
	// Reivindicação atômica: quem conseguir marcar auto_published_at é quem
	// publica. Duas réplicas competindo, ou um restart no meio, não geram post
	// duplicado — e post duplicado no Instagram não tem desfazer.
	var claimed string
	err := s.db.QueryRow(ctx,
		`UPDATE social_posts SET auto_published_at = NOW()
		 WHERE id = $1::uuid AND auto_published_at IS NULL
		 RETURNING id::text`, id).Scan(&claimed)
	if err != nil {
		return // outra instância pegou, ou o post mudou de estado
	}

	post, targets, adapters, caption, err := s.preparePublish(ctx, id, nil)
	if err != nil {
		// Pula e avisa: post incompleto (sem mídia, carrossel com 1 item) não
		// vai ao ar torto. Devolve a marca pra que, depois de corrigido, o
		// próprio agendador publique sem ninguém precisar lembrar.
		if _, uerr := s.db.Exec(ctx,
			`UPDATE social_posts SET auto_published_at = NULL WHERE id = $1::uuid`, id); uerr != nil {
			slog.Error("agendador social: falha ao liberar post pulado", "post_id", id, "err", uerr)
		}
		slog.Warn("agendador social: post agendado pulado", "post_id", id, "err", err)
		if _, nerr := s.insertSocialPostNote(ctx, id, agendadorUserID,
			fmt.Sprintf("Publicação automática pulada: %s", err.Error())); nerr != nil && !errors.Is(nerr, context.Canceled) {
			slog.Warn("agendador social: falha ao registrar nota", "post_id", id, "err", nerr)
		}
		return
	}
	slog.Info("agendador social: publicando post agendado", "post_id", id, "platforms", targets)
	s.runPublish(context.WithoutCancel(ctx), post, targets, adapters, agendadorUserID, caption)
}
