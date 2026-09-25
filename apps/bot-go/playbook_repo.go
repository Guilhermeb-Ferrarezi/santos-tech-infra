package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fichas de situação do playbook (migration 0047). Nada entra no bot sem um
// humano ativar; arquivar não apaga.

var (
	ErrSituacaoInvalida      = errors.New("ficha inválida")
	ErrSituacaoNaoEncontrada = errors.New("ficha não encontrada")
	ErrLimiteDeFichas        = errors.New("limite de fichas atingido")
)

const (
	maxTituloSituacao = 120
	maxCampoSituacao  = 1500
	maxFichas         = 200
)

// Situacao — uma ficha do playbook.
type Situacao struct {
	ID          string    `json:"id"`
	Titulo      string    `json:"titulo"`
	Sinais      string    `json:"sinais"`   // quando reconhecer
	PorTras     string    `json:"porTras"`  // o que costuma estar por trás
	Conduzir    string    `json:"conduzir"` // como conduzir
	Evitar      string    `json:"evitar"`   // o que evitar
	Motivos     []string  `json:"motivos"`  // motivacaoTipo; vazio = qualquer
	ParaQuem    string    `json:"paraQuem"` // proprio | filho | outro; vazio = qualquer
	CasoReal    string    `json:"casoReal"`
	ConversaID  string    `json:"conversaId,omitempty"`
	Estado      string    `json:"estado"` // rascunho | ativa | arquivada
	Origem      string    `json:"origem"` // manual | ia | whatsapp
	CriadoPor   string    `json:"criadoPor"`
	CriadoEm    time.Time `json:"criadoEm"`
	AlteradoPor string    `json:"alteradoPor"`
	AlteradoEm  time.Time `json:"alteradoEm"`
}

var (
	estadosDaFicha = map[string]bool{"rascunho": true, "ativa": true, "arquivada": true}
	origensDaFicha = map[string]bool{"manual": true, "ia": true, "whatsapp": true}
)

// normalizada tira espaços e preenche os padrões (rascunho, manual).
func (s Situacao) normalizada() Situacao {
	s.Titulo = strings.TrimSpace(s.Titulo)
	s.Sinais = strings.TrimSpace(s.Sinais)
	s.PorTras = strings.TrimSpace(s.PorTras)
	s.Conduzir = strings.TrimSpace(s.Conduzir)
	s.Evitar = strings.TrimSpace(s.Evitar)
	s.CasoReal = strings.TrimSpace(s.CasoReal)
	s.ParaQuem = strings.TrimSpace(s.ParaQuem)
	if s.Estado == "" {
		s.Estado = "rascunho"
	}
	if s.Origem == "" {
		s.Origem = "manual"
	}
	if s.Motivos == nil {
		s.Motivos = []string{}
	}
	return s
}

// Valida — mensagens em português: aparecem na tela.
func (s Situacao) Valida() error {
	s = s.normalizada()
	if s.Titulo == "" {
		return fmt.Errorf("a ficha precisa de um título")
	}
	if utf8.RuneCountInString(s.Titulo) > maxTituloSituacao {
		return fmt.Errorf("o título aceita no máximo %d caracteres", maxTituloSituacao)
	}
	for nome, campo := range map[string]string{
		"quando reconhecer": s.Sinais, "o que está por trás": s.PorTras,
		"como conduzir": s.Conduzir, "o que evitar": s.Evitar, "caso real": s.CasoReal,
	} {
		if utf8.RuneCountInString(campo) > maxCampoSituacao {
			return fmt.Errorf("o campo %q aceita no máximo %d caracteres", nome, maxCampoSituacao)
		}
	}
	for _, m := range s.Motivos {
		if _, ok := motivacoesValidas[m]; !ok {
			return fmt.Errorf("motivo desconhecido: %q", m)
		}
	}
	if s.ParaQuem != "" && !paraQuemValidos[s.ParaQuem] {
		return fmt.Errorf("\"para quem\" desconhecido: %q", s.ParaQuem)
	}
	if !estadosDaFicha[s.Estado] {
		return fmt.Errorf("estado desconhecido: %q", s.Estado)
	}
	if !origensDaFicha[s.Origem] {
		return fmt.Errorf("origem desconhecida: %q", s.Origem)
	}
	return nil
}

type PlaybookRepo struct{ pool *pgxpool.Pool }

const colunasSituacao = `id::text, titulo, sinais, por_tras, conduzir, evitar, motivos, para_quem,
	caso_real, COALESCE(conversa_id::text, ''), estado, origem, criado_por, criado_em, alterado_por, alterado_em`

func scanSituacao(row pgx.Row) (Situacao, error) {
	var s Situacao
	err := row.Scan(&s.ID, &s.Titulo, &s.Sinais, &s.PorTras, &s.Conduzir, &s.Evitar, &s.Motivos, &s.ParaQuem,
		&s.CasoReal, &s.ConversaID, &s.Estado, &s.Origem, &s.CriadoPor, &s.CriadoEm, &s.AlteradoPor, &s.AlteradoEm)
	return s, err
}

func (r *PlaybookRepo) consulta(ctx context.Context, sql string, args ...any) ([]Situacao, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Situacao{}
	for rows.Next() {
		s, err := scanSituacao(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Lista as fichas do tenant; estado "" = todas. Mais recentes primeiro.
func (r *PlaybookRepo) Lista(ctx context.Context, tenant TenantID, estado string) ([]Situacao, error) {
	out, err := r.consulta(ctx,
		`SELECT `+colunasSituacao+` FROM bot_playbook_situacao
		 WHERE tenant_id = $1 AND ($2 = '' OR estado = $2) ORDER BY alterado_em DESC`, tenant, estado)
	if err != nil {
		return nil, fmt.Errorf("PlaybookRepo.Lista: %w", err)
	}
	return out, nil
}

// Ativas — as que podem entrar no prompt.
func (r *PlaybookRepo) Ativas(ctx context.Context, tenant TenantID) ([]Situacao, error) {
	return r.Lista(ctx, tenant, "ativa")
}

// Cria grava uma ficha nova (rascunho, se o estado não vier).
func (r *PlaybookRepo) Cria(ctx context.Context, tenant TenantID, s Situacao, quem string) (Situacao, error) {
	if err := s.Valida(); err != nil {
		return Situacao{}, fmt.Errorf("%w: %s", ErrSituacaoInvalida, err.Error())
	}
	s = s.normalizada()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Situacao{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM bot_playbook_situacao WHERE tenant_id = $1`, tenant).Scan(&total); err != nil {
		return Situacao{}, fmt.Errorf("PlaybookRepo.Cria: %w", err)
	}
	if total >= maxFichas {
		return Situacao{}, ErrLimiteDeFichas
	}
	var conversa any
	if s.ConversaID != "" {
		conversa = s.ConversaID
	}
	nova, err := scanSituacao(tx.QueryRow(ctx,
		`INSERT INTO bot_playbook_situacao
		   (tenant_id, titulo, sinais, por_tras, conduzir, evitar, motivos, para_quem, caso_real,
		    conversa_id, estado, origem, criado_por, alterado_por)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)
		 RETURNING `+colunasSituacao,
		tenant, s.Titulo, s.Sinais, s.PorTras, s.Conduzir, s.Evitar, s.Motivos, s.ParaQuem, s.CasoReal,
		conversa, s.Estado, s.Origem, quem))
	if err != nil {
		return Situacao{}, fmt.Errorf("PlaybookRepo.Cria: %w", err)
	}
	if err := registraEvento(ctx, tx, tenant, nova.ID, "criou", quem); err != nil {
		return Situacao{}, err
	}
	if nova.Estado == "ativa" {
		if err := registraEvento(ctx, tx, tenant, nova.ID, "ativou", quem); err != nil {
			return Situacao{}, err
		}
	}
	return nova, tx.Commit(ctx)
}

// Edita troca o conteúdo e o estado de uma ficha. A origem e a conversa de
// origem não mudam: são de onde a ficha veio.
func (r *PlaybookRepo) Edita(ctx context.Context, tenant TenantID, id string, s Situacao, quem string) (Situacao, error) {
	if err := s.Valida(); err != nil {
		return Situacao{}, fmt.Errorf("%w: %s", ErrSituacaoInvalida, err.Error())
	}
	s = s.normalizada()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Situacao{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	antes, err := scanSituacao(tx.QueryRow(ctx,
		`SELECT `+colunasSituacao+` FROM bot_playbook_situacao
		 WHERE tenant_id = $1 AND id::text = $2 FOR UPDATE`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Situacao{}, ErrSituacaoNaoEncontrada
	}
	if err != nil {
		return Situacao{}, fmt.Errorf("PlaybookRepo.Edita: %w", err)
	}
	depois, err := scanSituacao(tx.QueryRow(ctx,
		`UPDATE bot_playbook_situacao SET
		   titulo = $3, sinais = $4, por_tras = $5, conduzir = $6, evitar = $7, motivos = $8,
		   para_quem = $9, caso_real = $10, estado = $11, alterado_por = $12, alterado_em = now()
		 WHERE tenant_id = $1 AND id::text = $2
		 RETURNING `+colunasSituacao,
		tenant, id, s.Titulo, s.Sinais, s.PorTras, s.Conduzir, s.Evitar, s.Motivos, s.ParaQuem, s.CasoReal, s.Estado, quem))
	if err != nil {
		return Situacao{}, fmt.Errorf("PlaybookRepo.Edita: %w", err)
	}
	acao := "editou"
	if antes.Estado != depois.Estado {
		acao = map[string]string{"ativa": "ativou", "arquivada": "arquivou", "rascunho": "voltou_rascunho"}[depois.Estado]
	}
	if err := registraEvento(ctx, tx, tenant, id, acao, quem); err != nil {
		return Situacao{}, err
	}
	return depois, tx.Commit(ctx)
}

func registraEvento(ctx context.Context, tx pgx.Tx, tenant TenantID, situacao, acao, quem string) error {
	// clock_timestamp, e não now(): "criou" e "ativou" na mesma transação
	// precisam sair em ordem na linha do tempo.
	if _, err := tx.Exec(ctx,
		`INSERT INTO bot_playbook_evento (tenant_id, situacao_id, acao, por, em)
		 VALUES ($1, $2, $3, $4, clock_timestamp())`, tenant, situacao, acao, quem); err != nil {
		return fmt.Errorf("PlaybookRepo: evento: %w", err)
	}
	return nil
}

// EventoPlaybook — uma linha da linha do tempo.
type EventoPlaybook struct {
	SituacaoID string    `json:"situacaoId"`
	Titulo     string    `json:"titulo"`
	Acao       string    `json:"acao"`
	Por        string    `json:"por"`
	Em         time.Time `json:"em"`
}

// Eventos — os mais novos primeiro.
func (r *PlaybookRepo) Eventos(ctx context.Context, tenant TenantID, limite int) ([]EventoPlaybook, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT e.situacao_id::text, s.titulo, e.acao, e.por, e.em
		 FROM bot_playbook_evento e JOIN bot_playbook_situacao s ON s.id = e.situacao_id
		 WHERE e.tenant_id = $1 ORDER BY e.em DESC LIMIT $2`, tenant, limite)
	if err != nil {
		return nil, fmt.Errorf("PlaybookRepo.Eventos: %w", err)
	}
	defer rows.Close()
	out := []EventoPlaybook{}
	for rows.Next() {
		var e EventoPlaybook
		if err := rows.Scan(&e.SituacaoID, &e.Titulo, &e.Acao, &e.Por, &e.Em); err != nil {
			return nil, fmt.Errorf("PlaybookRepo.Eventos: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RegistraUso grava que o bot usou estas fichas nesta conversa. Só grava ficha
// que é do tenant — id de fora é ignorado em silêncio (quem filtra o que o
// modelo inventou é o engine; isto é a segunda rede).
func (r *PlaybookRepo) RegistraUso(ctx context.Context, tenant TenantID, contato, conversa string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.pool.Exec(ctx,
		`INSERT INTO bot_playbook_uso (tenant_id, situacao_id, contact_id, conversation_id)
		 SELECT $1, s.id, $3::uuid, $4::uuid FROM bot_playbook_situacao s
		 WHERE s.tenant_id = $1 AND s.id::text = ANY($2)`, tenant, ids, contato, conversa)
	if err != nil {
		return fmt.Errorf("PlaybookRepo.RegistraUso: %w", err)
	}
	return nil
}
