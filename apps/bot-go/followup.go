package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// O que aconteceu depois da aula experimental.
//
// O bot sabe marcar a aula e esquece dela no minuto seguinte. Ninguém registra
// se a pessoa apareceu, e por isso ninguém sabe a única coisa que importa:
// quantos leads viraram aluno. Este arquivo é o que transforma "o bot está
// funcionando" de opinião em número.
//
// A LISTA NÃO É UMA TABELA. Ela é montada na hora a partir de booking_reminder
// (o livro-razão das aulas que o bot marcou) com LEFT JOIN no resultado. Aula
// marcada agora aparece na lista imediatamente, sem job de sincronia para
// esquecer de rodar, e "ainda não marcado" é a ausência de linha — um estado a
// menos para dar errado.

// ResultadoAula — os quatro desfechos que a escola separa hoje.
type ResultadoAula string

const (
	ResultadoFaltou    ResultadoAula = "faltou"
	ResultadoVeio      ResultadoAula = "veio"
	ResultadoFechou    ResultadoAula = "fechou"
	ResultadoNaoFechou ResultadoAula = "nao_fechou"
)

var resultadosLegiveis = map[ResultadoAula]string{
	ResultadoFaltou:    "faltou",
	ResultadoVeio:      "veio (ainda decidindo)",
	ResultadoFechou:    "veio e fechou",
	ResultadoNaoFechou: "veio e não fechou",
}

func ResultadoValido(s string) bool {
	_, ok := resultadosLegiveis[ResultadoAula(s)]
	return ok
}

func ResultadoLegivel(s string) string { return resultadosLegiveis[ResultadoAula(s)] }

// StatusAvaliacao — onde a pessoa está no convite para avaliar no Google.
const (
	AvaliacaoNaoPedido = "nao_pedido"
	AvaliacaoPedido    = "pedido"
	AvaliacaoAvaliou   = "avaliou"
	AvaliacaoRecusou   = "recusou"
	AvaliacaoNaoPedir  = "nao_pedir"
)

var statusAvaliacaoLegiveis = map[string]string{
	AvaliacaoNaoPedido: "ainda não convidado",
	AvaliacaoPedido:    "convidado, sem resposta",
	AvaliacaoAvaliou:   "avaliou",
	AvaliacaoRecusou:   "disse que não quer",
	AvaliacaoNaoPedir:  "não convidar",
}

func StatusAvaliacaoValido(s string) bool {
	_, ok := statusAvaliacaoLegiveis[s]
	return ok
}

func StatusAvaliacaoLegivel(s string) string {
	if s == "" {
		return statusAvaliacaoLegiveis[AvaliacaoNaoPedido]
	}
	return statusAvaliacaoLegiveis[s]
}

// LinhaFollowup — uma aula na lista do painel, com tudo que a coordenação
// precisa para decidir em um clique.
type LinhaFollowup struct {
	NotionPageID string     `json:"notionPageId"`
	Telefone     string     `json:"telefone"`
	Aluno        string     `json:"aluno"`
	AulaEm       time.Time  `json:"aulaEm"`
	ConversaID   string     `json:"conversaId,omitempty"`
	Resultado    string     `json:"resultado,omitempty"`
	MarcadoEm    *time.Time `json:"marcadoEm,omitempty"`
	MarcadoPor   string     `json:"marcadoPor,omitempty"`
	Observacao   string     `json:"observacao,omitempty"`

	// Do dossiê — para a coordenação não precisar abrir outra tela antes de
	// ligar para quem faltou.
	ContatoNome string `json:"contatoNome,omitempty"`
	Grau        string `json:"grau,omitempty"`
	Interesse   string `json:"interesse,omitempty"`
	Idade       int    `json:"idade,omitempty"`
	Origem      string `json:"origem,omitempty"`
	OrigemLabel string `json:"origemLabel,omitempty"`

	// Avaliação no Google desta PESSOA (não desta aula).
	Avaliacao      string `json:"avaliacao"`
	AvaliacaoLabel string `json:"avaliacaoLabel"`
}

type FollowupRepo struct{ pool *pgxpool.Pool }

func NewFollowupRepo(pool *pgxpool.Pool) *FollowupRepo { return &FollowupRepo{pool: pool} }

// Lista devolve as aulas do bot da mais recente para a mais antiga.
//
// DISTINCT ON no notion_page_id porque booking_reminder tem ATÉ TRÊS linhas por
// aula (véspera, 4h e 1h antes) — e uma aula marcada em cima da hora pode ter
// zero. Contar linhas ali daria três aulas onde há uma.
//
// `desde` corta o passado: a coordenação trabalha as últimas semanas, e carregar
// o histórico inteiro a cada abertura de tela só serve para a tela demorar.
func (r *FollowupRepo) Lista(ctx context.Context, tenantID TenantID, desde time.Time, limite int) ([]LinhaFollowup, error) {
	if limite <= 0 || limite > 500 {
		limite = 200
	}
	rows, err := r.pool.Query(ctx, `
		WITH aulas AS (
			SELECT DISTINCT ON (br.notion_page_id)
			       br.notion_page_id,
			       br.client_phone,
			       br.aluno,
			       br.aula_em,
			       coalesce(br.conversation_id::text, '') AS conversation_id
			FROM booking_reminder br
			WHERE br.tenant_id = $1
			  AND br.aula_em >= $2
			  AND br.status <> 'cancelado'
			ORDER BY br.notion_page_id, br.aula_em
		)
		SELECT a.notion_page_id, a.client_phone, a.aluno, a.aula_em, a.conversation_id,
		       coalesce(ar.resultado, ''), ar.marcado_em,
		       coalesce(ar.marcado_por, ''), coalesce(ar.observacao, ''),
		       coalesce(ct.display_name, ''),
		       coalesce(lq.interesse, ''), coalesce(lq.aluno_idade, 0),
		       coalesce(lq.origem, ''),
		       coalesce(lq.para_quem, ''), coalesce(lq.preco_informado, false),
		       coalesce(lq.aula_marcada, false), coalesce(lq.turnos_respondendo, 0),
		       coalesce(lq.aluno_nome, ''), coalesce(lq.ja_faz_curso, ''),
		       coalesce(lq.disponibilidade, ''), coalesce(lq.motivacao, ''),
		       coalesce(lq.motivacao_tipo, ''),
		       coalesce(av.status, '')
		FROM aulas a
		LEFT JOIN aula_resultado ar
		       ON ar.tenant_id = $1 AND ar.notion_page_id = a.notion_page_id
		LEFT JOIN avaliacao_google av
		       ON av.tenant_id = $1 AND av.client_phone = a.client_phone
		LEFT JOIN channel_identity ci
		       ON ci.tenant_id = $1 AND ci.external_id = a.client_phone
		LEFT JOIN contact ct
		       ON ct.id = ci.contact_id
		LEFT JOIN lead_qualificacao lq
		       ON lq.tenant_id = $1 AND lq.contact_id = ci.contact_id
		ORDER BY a.aula_em DESC
		LIMIT $3
	`, tenantID, desde, limite)
	if err != nil {
		return nil, fmt.Errorf("FollowupRepo.Lista: %w", err)
	}
	defer rows.Close()

	var out []LinhaFollowup
	for rows.Next() {
		var l LinhaFollowup
		var q Qualificacao
		var avStatus string
		if err := rows.Scan(
			&l.NotionPageID, &l.Telefone, &l.Aluno, &l.AulaEm, &l.ConversaID,
			&l.Resultado, &l.MarcadoEm, &l.MarcadoPor, &l.Observacao,
			&l.ContatoNome,
			&q.Interesse, &q.AlunoIdade, &l.Origem,
			&q.ParaQuem, &q.PrecoInformado, &q.AulaMarcada, &q.TurnosRespondendo,
			&q.AlunoNome, &q.JaFazCurso, &q.Disponibilidade, &q.Motivacao,
			&q.MotivacaoTipo,
			&avStatus,
		); err != nil {
			return nil, fmt.Errorf("FollowupRepo.Lista scan: %w", err)
		}
		l.Interesse = q.Interesse
		l.Idade = q.AlunoIdade
		l.Grau = string(q.Grau())
		l.OrigemLabel = OrigemLegivel(l.Origem)
		if avStatus == "" {
			avStatus = AvaliacaoNaoPedido
		}
		l.Avaliacao = avStatus
		l.AvaliacaoLabel = StatusAvaliacaoLegivel(avStatus)
		// O nome da aula é o que a coordenação reconhece; o do dossiê só entra
		// quando a aula não tem nenhum.
		if strings.TrimSpace(l.Aluno) == "" {
			l.Aluno = q.AlunoNome
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ErrAulaDesconhecida — tentaram marcar resultado de uma aula que o bot não marcou.
var ErrAulaDesconhecida = errors.New("followup: aula não encontrada")

// MarcaResultado grava o desfecho de uma aula.
//
// Copia telefone, aluno e data DA AULA em vez de referenciá-los: a aula pode ser
// arquivada no Notion e os lembretes apagados, e o resultado precisa continuar
// legível anos depois. É o registro histórico, não um ponteiro para um.
//
// Só aceita aula que exista em booking_reminder. Sem essa checagem, um id
// digitado errado no painel viraria uma linha órfã que nunca aparece na lista e
// nunca mais é corrigida.
func (r *FollowupRepo) MarcaResultado(ctx context.Context, tenantID TenantID, notionPageID, resultado, observacao, quem string) error {
	if !ResultadoValido(resultado) {
		return fmt.Errorf("followup: resultado inválido %q", resultado)
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO aula_resultado
		  (tenant_id, notion_page_id, client_phone, aluno, aula_em,
		   resultado, observacao, marcado_em, marcado_por)
		SELECT $1, a.notion_page_id, a.client_phone, a.aluno, a.aula_em,
		       $3, $4, now(), $5
		FROM (
			SELECT DISTINCT ON (br.notion_page_id)
			       br.notion_page_id, br.client_phone, br.aluno, br.aula_em
			FROM booking_reminder br
			WHERE br.tenant_id = $1 AND br.notion_page_id = $2
			ORDER BY br.notion_page_id, br.aula_em
		) a
		ON CONFLICT (tenant_id, notion_page_id) DO UPDATE SET
		  resultado   = EXCLUDED.resultado,
		  observacao  = EXCLUDED.observacao,
		  marcado_em  = now(),
		  marcado_por = EXCLUDED.marcado_por
	`, tenantID, notionPageID, resultado, observacao, quem)
	if err != nil {
		return fmt.Errorf("FollowupRepo.MarcaResultado: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAulaDesconhecida
	}
	return nil
}

// MarcaAvaliacao registra em que pé está o convite para avaliar no Google.
//
// ⚠️ `avaliou` é ANOTAÇÃO, não fato verificado: o Google não diz quem avaliou, e
// a avaliação pode estar com outro nome. Quem marca é uma pessoa que conferiu o
// perfil ou ouviu do cliente.
//
// Os carimbos de tempo são escritos pelo código a partir do status, e não vêm do
// painel: data que a interface manda é data que a interface pode errar, e aqui
// ela é a base de "já pedimos faz quanto tempo?".
func (r *FollowupRepo) MarcaAvaliacao(ctx context.Context, tenantID TenantID, telefone, status, observacao string) error {
	if !StatusAvaliacaoValido(status) {
		return fmt.Errorf("followup: status de avaliação inválido %q", status)
	}
	if strings.TrimSpace(telefone) == "" {
		return errors.New("followup: telefone vazio")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO avaliacao_google
		  (tenant_id, client_phone, aluno, status, observacao,
		   pedido_em, respondido_em, atualizado_em)
		VALUES ($1, $2,
		        coalesce((SELECT ct.display_name
		                    FROM channel_identity ci
		                    JOIN contact ct ON ct.id = ci.contact_id
		                   WHERE ci.tenant_id = $1 AND ci.external_id = $2
		                   LIMIT 1), ''),
		        $3, $4,
		        CASE WHEN $3 = 'pedido' THEN now() END,
		        CASE WHEN $3 IN ('avaliou','recusou') THEN now() END,
		        now())
		ON CONFLICT (tenant_id, client_phone) DO UPDATE SET
		  status     = EXCLUDED.status,
		  observacao = EXCLUDED.observacao,
		  -- pedido_em é a data do PRIMEIRO convite e não se reescreve: é dela
		  -- que sai "pedimos há dez dias e não houve resposta".
		  pedido_em  = coalesce(avaliacao_google.pedido_em, EXCLUDED.pedido_em),
		  -- lembrado_em marca a segunda (e última) cobrança.
		  lembrado_em = CASE
		    WHEN EXCLUDED.status = 'pedido' AND avaliacao_google.pedido_em IS NOT NULL
		      THEN now() ELSE avaliacao_google.lembrado_em END,
		  respondido_em = coalesce(EXCLUDED.respondido_em, avaliacao_google.respondido_em),
		  atualizado_em = now()
	`, tenantID, telefone, status, observacao)
	if err != nil {
		return fmt.Errorf("FollowupRepo.MarcaAvaliacao: %w", err)
	}
	return nil
}

// ResumoFollowup — a conta que a escola quer ver, e que hoje ninguém consegue
// fazer: de cada dez que marcaram, quantos vieram e quantos fecharam.
type ResumoFollowup struct {
	Total        int `json:"total"`
	SemResultado int `json:"semResultado"`
	Faltou       int `json:"faltou"`
	Veio         int `json:"veio"`
	Fechou       int `json:"fechou"`
	NaoFechou    int `json:"naoFechou"`

	// PorOrigem — de onde vieram os que FECHARAM. É o número que decide onde a
	// escola põe o dinheiro no mês seguinte.
	PorOrigem map[string]int `json:"porOrigem,omitempty"`
}

// Resume conta os desfechos da mesma lista que o painel mostra.
func Resume(linhas []LinhaFollowup) ResumoFollowup {
	res := ResumoFollowup{Total: len(linhas), PorOrigem: map[string]int{}}
	for _, l := range linhas {
		switch ResultadoAula(l.Resultado) {
		case ResultadoFaltou:
			res.Faltou++
		case ResultadoVeio:
			res.Veio++
		case ResultadoFechou:
			res.Fechou++
			chave := l.Origem
			if chave == "" {
				chave = "desconhecida"
			}
			res.PorOrigem[chave]++
		case ResultadoNaoFechou:
			res.NaoFechou++
		default:
			res.SemResultado++
		}
	}
	return res
}

// AguardandoConviteDeAvaliacao lista quem teve aula de verdade e ainda não foi
// convidado a avaliar.
//
// ⚠️ A CONSULTA É DE PROPÓSITO CEGA AO DESFECHO COMERCIAL: entra quem VEIO,
// tenha fechado ou não. Filtrar o convite por quem comprou (ou por quem elogiou)
// é "review gating" — proibido pelas políticas do Google e motivo de remoção do
// perfil. O que se controla aqui é não pedir duas vezes, não escolher a quem
// pedir.
func (r *FollowupRepo) AguardandoConviteDeAvaliacao(ctx context.Context, tenantID TenantID, limite int) ([]LinhaFollowup, error) {
	if limite <= 0 || limite > 200 {
		limite = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT ar.notion_page_id, ar.client_phone, ar.aluno, ar.aula_em, ar.resultado,
		       coalesce(av.status, '')
		FROM aula_resultado ar
		LEFT JOIN avaliacao_google av
		       ON av.tenant_id = $1 AND av.client_phone = ar.client_phone
		WHERE ar.tenant_id = $1
		  AND ar.resultado IN ('veio', 'fechou', 'nao_fechou')
		  AND coalesce(av.status, 'nao_pedido') = 'nao_pedido'
		ORDER BY ar.aula_em DESC
		LIMIT $2
	`, tenantID, limite)
	if err != nil {
		return nil, fmt.Errorf("FollowupRepo.AguardandoConviteDeAvaliacao: %w", err)
	}
	defer rows.Close()

	var out []LinhaFollowup
	for rows.Next() {
		var l LinhaFollowup
		var st string
		var aulaEm *time.Time
		if err := rows.Scan(&l.NotionPageID, &l.Telefone, &l.Aluno, &aulaEm, &l.Resultado, &st); err != nil {
			return nil, fmt.Errorf("FollowupRepo.AguardandoConviteDeAvaliacao scan: %w", err)
		}
		if aulaEm != nil {
			l.AulaEm = *aulaEm
		}
		l.Avaliacao = AvaliacaoNaoPedido
		l.AvaliacaoLabel = StatusAvaliacaoLegivel(AvaliacaoNaoPedido)
		out = append(out, l)
	}
	return out, rows.Err()
}
