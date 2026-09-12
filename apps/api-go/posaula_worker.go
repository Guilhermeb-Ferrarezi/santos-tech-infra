package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Pós-aula — o aviso "Sua prática está pronta". A prática é gravada na hora
// da geração com available_from = 07:00 do dia seguinte; este worker passa a
// cada 10 min e avisa o aluno das que acabaram de liberar. Molde do
// portal_chamada_worker.go: ticker in-process + trava no Redis pra uma
// réplica só varrer por vez. Idempotente: notified_at marca o que já foi
// avisado, então rodar de novo (ou duas réplicas sem Redis) no máximo
// repete um aviso, nunca perde um.

const (
	posaulaNotifIntervalo  = 10 * time.Minute
	posaulaNotifAtrasoBoot = time.Minute
	// posaulaNotifLockTTL menor que o intervalo: se a réplica que pegou a
	// trava morrer no meio, a próxima varredura (de qualquer réplica) já
	// encontra a trava expirada.
	posaulaNotifLockTTL = 8 * time.Minute
	posaulaNotifLockKey = "api-go:posaula:notificar"
	// posaulaNotifURL é a tela do aluno no dashboard (rota /pratica), no
	// formato dos outros chamadores de notifyUser ("/dashboard/tarefas",
	// "/dashboard/email"): caminho absoluto no site. É o que o service worker
	// do push exige (só abre URL dentro do escopo /dashboard/); o sino tira o
	// prefixo antes de navegar.
	posaulaNotifURL = "/dashboard/pratica"
)

func (s *Server) startPosaulaWorker(ctx context.Context) {
	if s.portalDB == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(posaulaNotifAtrasoBoot):
		}
		s.posaulaNotificarComLock(ctx)
		t := time.NewTicker(posaulaNotifIntervalo)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.posaulaNotificarComLock(ctx)
			}
		}
	}()
}

func (s *Server) posaulaNotificarComLock(ctx context.Context) {
	if s.rdb != nil {
		ok, err := s.rdb.SetNX(ctx, posaulaNotifLockKey, "1", posaulaNotifLockTTL).Result()
		if err != nil {
			slog.Warn("posaula: trava indisponível, notificando mesmo assim", "err", err)
		} else if !ok {
			return
		}
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	n, err := s.posaulaNotificarLiberadas(c)
	if err != nil {
		slog.Error("posaula: falha ao notificar práticas liberadas", "err", err)
		return
	}
	if n > 0 {
		slog.Info("posaula: alunos avisados de prática liberada", "avisos", n)
	}
}

// posaulaLote é um grupo (aluno, aula) de práticas liberadas e ainda não
// avisadas — um aviso por aula, não por prática.
type posaulaLote struct {
	userID  int64
	email   string
	data    string // dd/mm
	taskIDs []int64
}

// posaulaNotificarLiberadas acha as práticas liberadas sem aviso, manda o
// sino/push pro usuário do auth central (casado por e-mail, como todo
// self-service do portal) e marca notified_at. Aluno sem conta no auth
// (não deveria acontecer — o cadastro cria as duas) é marcado mesmo assim,
// senão o worker tentaria pra sempre a cada 10 min.
func (s *Server) posaulaNotificarLiberadas(ctx context.Context) (int, error) {
	rows, err := s.portalDB.Query(ctx, `
		SELECT pt.user_id, COALESCE(u.email,''), to_char(cs.date,'DD/MM'), array_agg(pt.id::bigint ORDER BY pt.id)
		FROM posaula_task pt
		JOIN class_session cs ON cs.id = pt.session_id
		LEFT JOIN "user" u ON u.id = pt.user_id
		WHERE pt.available_from <= now() AND pt.notified_at IS NULL AND pt.deleted_at IS NULL
		GROUP BY pt.user_id, u.email, cs.date, pt.session_id
		ORDER BY pt.user_id, cs.date`)
	if err != nil {
		return 0, err
	}
	var lotes []posaulaLote
	for rows.Next() {
		var l posaulaLote
		if err := rows.Scan(&l.userID, &l.email, &l.data, &l.taskIDs); err != nil {
			rows.Close()
			return 0, err
		}
		lotes = append(lotes, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	avisos := 0
	for _, l := range lotes {
		email := strings.ToLower(strings.TrimSpace(l.email))
		if email != "" && s.db != nil {
			u, err := s.userByEmail(ctx, email)
			if err != nil {
				return avisos, fmt.Errorf("buscar usuário do auth: %w", err)
			}
			if u != nil {
				s.notifyUser(ctx, int32(u.ID), "Sua prática está pronta", posaulaNotifCorpo(len(l.taskIDs), l.data), posaulaNotifURL)
				avisos++
			} else {
				slog.Warn("posaula: aluno sem conta no auth central, aviso pulado", "portalUser", l.userID)
			}
		} else {
			slog.Warn("posaula: aluno sem e-mail no Portal, aviso pulado", "portalUser", l.userID)
		}
		if _, err := s.portalDB.Exec(ctx, `UPDATE posaula_task SET notified_at = now() WHERE id = ANY($1::bigint[]) AND notified_at IS NULL`, l.taskIDs); err != nil {
			return avisos, fmt.Errorf("marcar notified_at: %w", err)
		}
	}
	return avisos, nil
}

// posaulaNotifCorpo — "3 práticas da aula de 11/09" / "1 prática da aula de 11/09".
func posaulaNotifCorpo(n int, data string) string {
	if n == 1 {
		return "1 prática da aula de " + data
	}
	return fmt.Sprintf("%d práticas da aula de %s", n, data)
}
