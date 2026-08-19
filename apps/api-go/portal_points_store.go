package main

import (
	"context"
	"fmt"
	"log/slog"
)

// portalMinAnswersForRanking é o piso de elegibilidade pro ranking de notas —
// evita que um aluno com 1-2 respostas (sorte/azar) apareça no topo/fundo do
// ranking. Mesmo valor usado pelo sistema antigo (.NET) pra premiação por notas.
const portalMinAnswersForRanking = 10

// grantExercisePointsIfComplete concede pontos ao aluno por um exercício assim
// que TODAS as questões dele estiverem corrigidas (is_correct preenchido) pra
// aquele aluno. Idempotente por (user_id, reason) via UNIQUE INDEX — chamar de
// novo pra um exercício já pontuado é no-op, então correções subsequentes (ex.:
// professor edita uma resposta depois) não duplicam o crédito.
//
// ponytail: sem penalidade por resposta fora do prazo (o sistema antigo reduzia
// 40% dos pontos se respondido após exercise.term_at) — aqui é sempre
// pointsRedeem * (corretas/total). Upgrade se isso for necessário: comparar
// answer.answered_at com exercise.term_at por resposta.
func (s *Server) grantExercisePointsIfComplete(ctx context.Context, userID, exerciseID int64) error {
	var totalQuestions int
	if err := s.portalDB.QueryRow(ctx, `SELECT COUNT(*) FROM question WHERE exercise_id=$1`, exerciseID).Scan(&totalQuestions); err != nil {
		return err
	}
	if totalQuestions == 0 {
		return nil
	}
	var corrected, correct int
	if err := s.portalDB.QueryRow(ctx, `SELECT
		COUNT(*) FILTER (WHERE is_correct IS NOT NULL),
		COUNT(*) FILTER (WHERE is_correct IS TRUE)
		FROM answer WHERE exercise_id=$1 AND user_id=$2`, exerciseID, userID).Scan(&corrected, &correct); err != nil {
		return err
	}
	if corrected < totalQuestions {
		return nil // ainda falta corrigir alguma questão desse aluno nesse exercício
	}
	var pointsRedeem float64
	if err := s.portalDB.QueryRow(ctx, `SELECT COALESCE(points_redeem,0) FROM exercise WHERE id=$1`, exerciseID).Scan(&pointsRedeem); err != nil {
		return err
	}
	points := pointsRedeem * float64(correct) / float64(totalQuestions)
	if points <= 0 {
		return nil
	}
	_, err := s.portalDB.Exec(ctx, `INSERT INTO portal_point (user_id, exercise_id, points, reason)
		VALUES ($1,$2,$3,$4) ON CONFLICT (user_id, reason) DO NOTHING`,
		userID, exerciseID, points, fmt.Sprintf("exercise:%d", exerciseID))
	return err
}

// grantPointsForAnswers concede pontos (melhor esforço, fora da transação
// principal de correção — falha aqui não deve derrubar a resposta HTTP de
// correção) pra cada par (aluno, exercício) distinto entre as respostas cujo id
// está em answerIDs.
func (s *Server) grantPointsForAnswers(ctx context.Context, answerIDs []int64) {
	if len(answerIDs) == 0 {
		return
	}
	rows, err := s.portalDB.Query(ctx, `SELECT DISTINCT user_id, exercise_id FROM answer WHERE id = ANY($1)`, answerIDs)
	if err != nil {
		slog.Error("ranking: falha ao buscar pares aluno/exercício pra pontos", "err", err)
		return
	}
	type pair struct{ userID, exerciseID int64 }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.userID, &p.exerciseID); err != nil {
			slog.Error("ranking: falha ao ler par aluno/exercício", "err", err)
			rows.Close()
			return
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	for _, p := range pairs {
		if err := s.grantExercisePointsIfComplete(ctx, p.userID, p.exerciseID); err != nil {
			slog.Error("ranking: falha ao conceder pontos", "err", err, "userId", p.userID, "exerciseId", p.exerciseID)
		}
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
