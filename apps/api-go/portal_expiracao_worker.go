package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Avisos de expiração do pacote de aula particular (Pós-aula, fase 3).
//
// Aula particular tem validade de 12 meses a partir da data do contrato
// (enrollment.contract_date; sem ela, o início da turma). A escola avisa o
// aluno e os administradores aos 60 e aos 30 dias antes de vencer — sino do
// dashboard e push, pelo notifyUser de sempre. Molde do
// portal_chamada_worker.go: ticker in-process a cada 6 h + trava no Redis
// pra uma réplica só varrer por vez. Idempotente de verdade pelas colunas
// expiry_notice_60_at/30_at: cada aviso sai uma vez por matrícula, e um
// contrato que já venceu quando o worker olhou pela primeira vez é só
// marcado (ninguém quer aviso atrasado de contrato antigo).

const (
	expiracaoIntervalo  = 6 * time.Hour
	expiracaoAtrasoBoot = 3 * time.Minute
	expiracaoLockTTL    = 30 * time.Minute
	expiracaoLockKey    = "api-go:portal:pacote-expiracao"
	// pacoteValidadeMeses: validade do pacote de aula particular.
	pacoteValidadeMeses = 12
	// expiracaoURLAluno / expiracaoURLTurma: pra onde o sino leva. Mesmo
	// formato dos outros chamadores de notifyUser ("/dashboard/tarefas"):
	// caminho absoluto no site, que o service worker do push exige e o sino
	// tira o prefixo antes de navegar.
	expiracaoURLAluno = "/dashboard/"
	expiracaoURLTurma = "/dashboard/admin/portal/turmas/"
)

// expiracaoAvisos são os marcos, em dias antes do vencimento, do mais
// distante pro mais próximo.
var expiracaoAvisos = []int{60, 30}

// ── Regras puras ─────────────────────────────────────────────────────────────

// pacoteVencimento: (data do contrato, senão início da turma) + 12 meses.
// Datas sem hora (meia-noite UTC, como o pgx entrega DATE). nil sem nenhuma
// das duas.
func pacoteVencimento(contractDate, classStart *time.Time) *time.Time {
	base := contractDate
	if base == nil {
		base = classStart
	}
	if base == nil {
		return nil
	}
	v := soData(*base).AddDate(0, pacoteValidadeMeses, 0)
	return &v
}

// pacoteVenceEm é o packageExpiresAt dos DTOs (aaaa-mm-dd): SÓ pra matrícula
// em turma de aula particular (class.individual_class); turma de grupo não
// tem pacote com validade.
func pacoteVenceEm(individualClass bool, contractDate, classStart *time.Time) *string {
	if !individualClass {
		return nil
	}
	v := pacoteVencimento(contractDate, classStart)
	if v == nil {
		return nil
	}
	s := v.Format("2006-01-02")
	return &s
}

// dataISO formata uma data do banco como aaaa-mm-dd (nil → nil).
func dataISO(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := soData(*t).Format("2006-01-02")
	return &s
}

// soData zera a hora, mantendo o dia do calendário do fuso em que t está.
func soData(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// hojeNaEscola é a data de hoje em America/Sao_Paulo, como data pura — às
// 22h em Ribeirão Preto o UTC já é amanhã, e "faltam 30 dias" tem que ser
// contado no calendário da escola.
func hojeNaEscola(now time.Time) time.Time {
	return soData(now.In(posaulaLocation()))
}

// diasAte conta os dias de calendário de hoje até a data (negativo = já passou).
func diasAte(hoje, data time.Time) int {
	return int(soData(data).Sub(soData(hoje)).Hours() / 24)
}

// avisosDevidos diz quais marcos (60, 30) devem ser MARCADOS agora: os que
// já chegaram e ainda não foram registrados. Vencimento passado devolve os
// dois — o chamador decide não avisar nesse caso (ver
// portalNotificarExpiracao). Pura, testável.
func avisosDevidos(hoje, vencimento time.Time, avisado60, avisado30 bool) []int {
	dias := diasAte(hoje, vencimento)
	var out []int
	if !avisado60 && dias <= 60 {
		out = append(out, 60)
	}
	if !avisado30 && dias <= 30 {
		out = append(out, 30)
	}
	return out
}

// expiracaoQuando — "hoje", "amanhã" ou "em N dias": vai no título do aviso.
func expiracaoQuando(dias int) string {
	switch {
	case dias <= 0:
		return "hoje"
	case dias == 1:
		return "amanhã"
	}
	return fmt.Sprintf("em %d dias", dias)
}

var mesesPorExtenso = [...]string{"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"}

// dataPorExtenso — "11 de novembro de 2026".
func dataPorExtenso(t time.Time) string {
	return fmt.Sprintf("%d de %s de %d", t.Day(), mesesPorExtenso[t.Month()-1], t.Year())
}

// ── Worker ───────────────────────────────────────────────────────────────────

func (s *Server) startExpiracaoWorker(ctx context.Context) {
	if s.portalDB == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(expiracaoAtrasoBoot):
		}
		s.portalNotificarExpiracaoComLock(ctx)
		t := time.NewTicker(expiracaoIntervalo)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.portalNotificarExpiracaoComLock(ctx)
			}
		}
	}()
}

func (s *Server) portalNotificarExpiracaoComLock(ctx context.Context) {
	if s.rdb != nil {
		ok, err := s.rdb.SetNX(ctx, expiracaoLockKey, "1", expiracaoLockTTL).Result()
		if err != nil {
			slog.Warn("expiração: trava indisponível, varrendo mesmo assim", "err", err)
		} else if !ok {
			return
		}
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	n, err := s.portalNotificarExpiracao(c, time.Now())
	if err != nil {
		slog.Error("expiração: falha ao avisar vencimentos de pacote", "err", err)
		return
	}
	if n > 0 {
		slog.Info("expiração: avisos de vencimento de pacote enviados", "avisos", n)
	}
}

// expiracaoMatricula é uma matrícula particular ainda com aviso pendente.
type expiracaoMatricula struct {
	userID, classID int64
	email, nome     string
	turma           string
	contractDate    *time.Time
	classStart      *time.Time
	avisado60       bool
	avisado30       bool
}

// portalNotificarExpiracao varre as matrículas em turma particular que ainda
// têm algum marco sem registro, avisa quem precisa e marca as colunas.
// Devolve quantos avisos (aluno + administradores) saíram.
func (s *Server) portalNotificarExpiracao(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.portalDB.Query(ctx, `
		SELECT e.user_id, e.class_id, COALESCE(u.email,''), COALESCE(u.name,''), COALESCE(cl.name,''),
		       e.contract_date, cl.start_date::date,
		       e.expiry_notice_60_at IS NOT NULL, e.expiry_notice_30_at IS NOT NULL
		FROM enrollment e
		JOIN class cl ON cl.id = e.class_id AND cl.individual_class
		LEFT JOIN "user" u ON u.id = e.user_id
		WHERE e.expiry_notice_60_at IS NULL OR e.expiry_notice_30_at IS NULL
		ORDER BY e.class_id, e.user_id`)
	if err != nil {
		return 0, err
	}
	var itens []expiracaoMatricula
	for rows.Next() {
		var m expiracaoMatricula
		if err := rows.Scan(&m.userID, &m.classID, &m.email, &m.nome, &m.turma, &m.contractDate, &m.classStart, &m.avisado60, &m.avisado30); err != nil {
			rows.Close()
			return 0, err
		}
		itens = append(itens, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(itens) == 0 {
		return 0, nil
	}

	hoje := hojeNaEscola(now)
	avisos := 0
	var admins []int64
	adminsCarregados := false
	for _, m := range itens {
		venc := pacoteVencimento(m.contractDate, m.classStart)
		if venc == nil {
			continue
		}
		devidos := avisosDevidos(hoje, *venc, m.avisado60, m.avisado30)
		if len(devidos) == 0 {
			continue
		}
		// Marca PRIMEIRO — é a marcação (UPDATE condicional, WHERE col IS
		// NULL) que é a trava de idempotência, não uma etapa que roda depois
		// do aviso: se o processo cair entre notificar e marcar, o reprocessa-
		// mento da varredura seguinte reenviaria o mesmo aviso pra todo mundo.
		// Com a marcação primeiro, o pior caso é o oposto — perder um aviso
		// isolado numa falha rara — nunca dobrar.
		marcouAlgo := false
		for _, marco := range devidos {
			col := fmt.Sprintf("expiry_notice_%d_at", marco)
			var afetou int
			err := s.portalDB.QueryRow(ctx, `UPDATE enrollment SET `+col+` = now() WHERE class_id = $1 AND user_id = $2 AND `+col+` IS NULL RETURNING 1`, m.classID, m.userID).Scan(&afetou)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return avisos, fmt.Errorf("marcar %s: %w", col, err)
			}
			if err == nil {
				marcouAlgo = true
			}
		}
		dias := diasAte(hoje, *venc)
		// Um aviso só por varredura, mesmo que os dois marcos tenham chegado
		// juntos (contrato lançado atrasado): "vence em 20 dias" já diz tudo.
		// Vencimento já passado (dias < 0): marca sem avisar — ninguém quer
		// aviso atrasado de contrato antigo. marcouAlgo=false (outro worker
		// já marcou entre o SELECT e aqui): nada a notificar.
		if dias >= 0 && marcouAlgo {
			if !adminsCarregados {
				if admins, err = s.listAdminIDs(ctx); err != nil {
					return avisos, fmt.Errorf("listar administradores: %w", err)
				}
				adminsCarregados = true
			}
			n, err := s.expiracaoAvisar(ctx, m, dias, *venc, admins)
			if err != nil {
				return avisos, err
			}
			avisos += n
		}
	}
	return avisos, nil
}

// expiracaoAvisar manda o aviso pro aluno (usuário do auth casado pelo
// e-mail do "user" do Portal) e pra todos os administradores. Aluno sem
// conta no auth é pulado com aviso no log — a matrícula é marcada mesmo
// assim, senão o worker tentaria pra sempre.
func (s *Server) expiracaoAvisar(ctx context.Context, m expiracaoMatricula, dias int, venc time.Time, admins []int64) (int, error) {
	quando := expiracaoQuando(dias)
	data := dataPorExtenso(venc)
	n := 0
	email := strings.ToLower(strings.TrimSpace(m.email))
	if email != "" && s.db != nil {
		u, err := s.userByEmail(ctx, email)
		if err != nil {
			return 0, fmt.Errorf("buscar aluno no auth: %w", err)
		}
		if u != nil {
			s.notifyUser(ctx, int32(u.ID), "Seu pacote de aulas vence "+quando,
				fmt.Sprintf("Seu pacote de aulas particulares vence em %s. Fale com a escola pra renovar.", data), expiracaoURLAluno)
			n++
		} else {
			slog.Warn("expiração: aluno sem conta no auth central, aviso pulado", "portalUser", m.userID, "class", m.classID)
		}
	} else {
		slog.Warn("expiração: aluno sem e-mail no Portal, aviso pulado", "portalUser", m.userID, "class", m.classID)
	}
	nome := vazioOu(strings.TrimSpace(m.nome), "aluno sem nome")
	turma := vazioOu(strings.TrimSpace(m.turma), fmt.Sprintf("Turma %d", m.classID))
	for _, id := range admins {
		s.notifyUser(ctx, int32(id), fmt.Sprintf("Pacote de %s vence %s", nome, quando),
			fmt.Sprintf("O pacote de aulas particulares de %s (%s) vence em %s.", nome, turma, data),
			fmt.Sprintf("%s%d", expiracaoURLTurma, m.classID))
		n++
	}
	return n, nil
}

// listAdminIDs: os administradores ATIVOS do auth central (role admin, não
// suspensos, com login). É quem recebe o aviso de vencimento junto com o aluno.
func (s *Server) listAdminIDs(ctx context.Context) ([]int64, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM users WHERE role = $1 AND suspended_at IS NULL AND NOT login_disabled ORDER BY id`, int16(RoleAdmin))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
