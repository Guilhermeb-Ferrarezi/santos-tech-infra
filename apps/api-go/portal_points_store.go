package main

import (
	"context"
	"log/slog"
)

// portalMinAnswersForRanking é o piso de elegibilidade pro ranking de notas —
// evita que um aluno com 1-2 respostas (sorte/azar) apareça no topo/fundo do
// ranking. Mesmo valor usado pelo sistema antigo (.NET) pra premiação por notas.
const portalMinAnswersForRanking = 10

// grantPointsForAnswers concede pontos pelos exercícios que ficaram COMPLETOS
// (todas as questões do aluno corrigidas) entre as respostas de answerIDs.
//
// É melhor esforço: roda fora da transação de correção e uma falha aqui não
// derruba a resposta HTTP — o crédito é recuperável numa correção posterior,
// porque é idempotente por (user_id, reason) via UNIQUE INDEX.
//
// Tudo acontece num ÚNICO statement. Antes eram 1 SELECT de pares + 3 queries
// e 1 INSERT POR PAR, na thread do request: um lote de 500 respostas podia
// virar ~2.500 round-trips e estourar o WriteTimeout de 60s.
//
// ponytail: sem penalidade por resposta fora do prazo (o sistema antigo
// reduzia 40% dos pontos se respondido após exercise.term_at) — aqui é sempre
// points_redeem * (corretas/total).
func (s *Server) grantPointsForAnswers(ctx context.Context, answerIDs []int64) {
	if len(answerIDs) == 0 {
		return
	}
	if _, err := s.portalDB.Exec(ctx, `
		INSERT INTO portal_point (user_id, exercise_id, points, reason)
		SELECT t.user_id, t.exercise_id,
			COALESCE(e.points_redeem, 0)::numeric * t.corretas / t.total_questoes,
			'exercise:' || t.exercise_id
		FROM (
			SELECT p.user_id, p.exercise_id, tq.total_questoes,
				COUNT(*) FILTER (WHERE a.is_correct IS NOT NULL) AS corrigidas,
				COUNT(*) FILTER (WHERE a.is_correct IS TRUE) AS corretas
			FROM (SELECT DISTINCT user_id, exercise_id FROM answer WHERE id = ANY($1)) p
			JOIN LATERAL (SELECT COUNT(*) AS total_questoes FROM question q WHERE q.exercise_id = p.exercise_id) tq ON true
			JOIN answer a ON a.user_id = p.user_id AND a.exercise_id = p.exercise_id
			GROUP BY p.user_id, p.exercise_id, tq.total_questoes
		) t
		JOIN exercise e ON e.id = t.exercise_id
		WHERE t.total_questoes > 0
		  AND t.corrigidas >= t.total_questoes
		  AND t.corretas > 0
		  AND COALESCE(e.points_redeem, 0) > 0
		ON CONFLICT (user_id, reason) DO NOTHING`, answerIDs); err != nil {
		slog.Error("ranking: falha ao conceder pontos", "err", err, "answers", len(answerIDs))
	}
}

// ── Rankings ─────────────────────────────────────────────────────────────────

// portalRankingNotaDTO — leaderboard não carrega e-mail: a rota é liberada a
// qualquer cargo com portal_rankings:read e um placar não precisa de PII de
// contato para ordenar alunos.
type portalRankingNotaDTO struct {
	StudentID      string  `json:"studentId"`
	Name           string  `json:"name"`
	TotalAnswers   int64   `json:"totalAnswers"`
	CorrectAnswers int64   `json:"correctAnswers"`
	PercentCorrect float64 `json:"percentCorrect"`
}

type portalRankingPontoDTO struct {
	StudentID   string  `json:"studentId"`
	Name        string  `json:"name"`
	TotalPoints float64 `json:"totalPoints"`
}

// portalRankingNotas: leaderboard global por % de acerto (não por curso/fase —
// o modelo novo não tem o conceito de "categoria" plana que o sistema antigo
// usava). Só entram alunos com >= portalMinAnswersForRanking respostas corrigidas.
func (s *Server) portalRankingNotas(ctx context.Context, p portalPagination) ([]portalRankingNotaDTO, int64, error) {
	var total int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM (
		SELECT a.user_id FROM answer a
		GROUP BY a.user_id
		HAVING COUNT(*) FILTER (WHERE a.is_correct IS NOT NULL) >= $1
	) t`, portalMinAnswersForRanking).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.portalDB.Query(ctx, `SELECT u.id::text, COALESCE(u.name,''),
		COUNT(*) FILTER (WHERE a.is_correct IS NOT NULL) AS total_respondidas,
		COUNT(*) FILTER (WHERE a.is_correct IS TRUE) AS corretas
		FROM answer a JOIN "user" u ON u.id = a.user_id
		GROUP BY u.id, u.name
		HAVING COUNT(*) FILTER (WHERE a.is_correct IS NOT NULL) >= $1
		ORDER BY (COUNT(*) FILTER (WHERE a.is_correct IS TRUE))::float
			/ NULLIF(COUNT(*) FILTER (WHERE a.is_correct IS NOT NULL), 0) DESC,
			total_respondidas DESC, u.id ASC
		LIMIT $2 OFFSET $3`, portalMinAnswersForRanking, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalRankingNotaDTO{}
	for rows.Next() {
		var dto portalRankingNotaDTO
		if err := rows.Scan(&dto.StudentID, &dto.Name, &dto.TotalAnswers, &dto.CorrectAnswers); err != nil {
			return nil, 0, err
		}
		if dto.TotalAnswers > 0 {
			dto.PercentCorrect = roundPercent(float64(dto.CorrectAnswers) / float64(dto.TotalAnswers) * 100)
		}
		items = append(items, dto)
	}
	return items, total, rows.Err()
}

// portalRankingPontos: leaderboard global por soma de portal_point.points,
// histórico completo (sem filtro de período — mesmo comportamento do antigo).
func (s *Server) portalRankingPontos(ctx context.Context, p portalPagination) ([]portalRankingPontoDTO, int64, error) {
	var total int64
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(DISTINCT user_id) FROM portal_point`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.portalDB.Query(ctx, `SELECT u.id::text, COALESCE(u.name,''), SUM(pp.points) AS total_points
		FROM portal_point pp JOIN "user" u ON u.id = pp.user_id
		GROUP BY u.id, u.name
		ORDER BY total_points DESC, u.id ASC
		LIMIT $1 OFFSET $2`, p.Limit, p.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []portalRankingPontoDTO{}
	for rows.Next() {
		var dto portalRankingPontoDTO
		if err := rows.Scan(&dto.StudentID, &dto.Name, &dto.TotalPoints); err != nil {
			return nil, 0, err
		}
		items = append(items, dto)
	}
	return items, total, rows.Err()
}

func roundPercent(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
