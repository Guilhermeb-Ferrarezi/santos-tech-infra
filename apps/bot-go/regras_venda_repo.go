package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Versões das regras de venda (migration 0044). Só inserção: salvar e
// restaurar gravam uma versão nova; nada é apagado.

var (
	// ErrVersaoDesatualizada — alguém salvou depois que a tela abriu (409).
	ErrVersaoDesatualizada = errors.New("existe uma versão mais nova das regras")
	// ErrVersaoNaoEncontrada — id que não existe ou é de outro tenant (404).
	ErrVersaoNaoEncontrada = errors.New("versão das regras não encontrada")
	// ErrRegrasInvalidas — embrulha a mensagem de RegrasVenda.Valida (400).
	ErrRegrasInvalidas = errors.New("regras inválidas")
)

// VersaoRegras — uma versão salva. ID vazio = nada salvo ainda (vale o padrão).
type VersaoRegras struct {
	ID           string                `json:"id"`
	Regras       RegrasVenda           `json:"regras"`
	PadraoHash   map[ParteRegra]string `json:"padraoHash"`
	Alteracoes   []string              `json:"alteracoes"`
	SalvoPor     string                `json:"salvoPor"`
	SalvoEm      time.Time             `json:"salvoEm"`
	RestauradaDe string                `json:"restauradaDe,omitempty"`
}

type RegrasVendaRepo struct{ pool *pgxpool.Pool }

const colunasVersaoRegras = `id::text, conteudo, padrao_hash, alteracoes, salvo_por, salvo_em, COALESCE(restaurada_de::text, '')`

func scanVersaoRegras(row pgx.Row) (VersaoRegras, error) {
	var v VersaoRegras
	var conteudo, hash []byte
	if err := row.Scan(&v.ID, &conteudo, &hash, &v.Alteracoes, &v.SalvoPor, &v.SalvoEm, &v.RestauradaDe); err != nil {
		return VersaoRegras{}, err
	}
	if err := json.Unmarshal(conteudo, &v.Regras); err != nil {
		return VersaoRegras{}, fmt.Errorf("conteúdo da versão %s: %w", v.ID, err)
	}
	if err := json.Unmarshal(hash, &v.PadraoHash); err != nil {
		return VersaoRegras{}, fmt.Errorf("hash da versão %s: %w", v.ID, err)
	}
	return v, nil
}

// Atual devolve a versão em vigor. Sem nada salvo: VersaoRegras vazia (o
// chamador resolve com o padrão), sem erro.
func (r *RegrasVendaRepo) Atual(ctx context.Context, tenant TenantID) (VersaoRegras, error) {
	return r.atual(ctx, r.pool, tenant)
}

type consultavel interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (r *RegrasVendaRepo) atual(ctx context.Context, q consultavel, tenant TenantID) (VersaoRegras, error) {
	v, err := scanVersaoRegras(q.QueryRow(ctx,
		`SELECT `+colunasVersaoRegras+` FROM bot_regras_venda_versao
		 WHERE tenant_id = $1 ORDER BY seq DESC LIMIT 1`, tenant))
	if errors.Is(err, pgx.ErrNoRows) {
		return VersaoRegras{}, nil
	}
	if err != nil {
		return VersaoRegras{}, fmt.Errorf("RegrasVendaRepo.Atual: %w", err)
	}
	return v, nil
}

// Salvar grava uma versão nova, se versaoBase ainda for a atual ("" = a tela
// abriu sem nada salvo). O documento é validado e gravado sem o que é igual
// ao padrão.
func (r *RegrasVendaRepo) Salvar(ctx context.Context, tenant TenantID, regras RegrasVenda, versaoBase, quem string) (VersaoRegras, error) {
	if err := regras.Valida(); err != nil {
		return VersaoRegras{}, fmt.Errorf("%w: %s", ErrRegrasInvalidas, err.Error())
	}
	return r.grava(ctx, tenant, regras.SemPadrao(), &versaoBase, quem, "")
}

// Restaurar grava uma cópia de uma versão antiga como a versão atual.
func (r *RegrasVendaRepo) Restaurar(ctx context.Context, tenant TenantID, id, quem string) (VersaoRegras, error) {
	antiga, err := r.Versao(ctx, tenant, id)
	if err != nil {
		return VersaoRegras{}, err
	}
	return r.grava(ctx, tenant, antiga.Regras, nil, quem, antiga.ID)
}

// grava insere a versão nova sob um lock por tenant. Sem o lock, dois
// primeiros saves simultâneos passariam os dois pela checagem de versaoBase
// (não há linha para travar com FOR UPDATE). versaoBase nil = não checar
// (restauração é uma ação explícita sobre uma versão escolhida).
func (r *RegrasVendaRepo) grava(ctx context.Context, tenant TenantID, doc RegrasVenda, versaoBase *string, quem, restauradaDe string) (VersaoRegras, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return VersaoRegras{}, fmt.Errorf("RegrasVendaRepo: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('regras_venda:' || $1::text))`, tenant); err != nil {
		return VersaoRegras{}, fmt.Errorf("RegrasVendaRepo: lock: %w", err)
	}
	anterior, err := r.atual(ctx, tx, tenant)
	if err != nil {
		return VersaoRegras{}, err
	}
	if versaoBase != nil && *versaoBase != anterior.ID {
		return VersaoRegras{}, ErrVersaoDesatualizada
	}

	conteudo, err := json.Marshal(doc)
	if err != nil {
		return VersaoRegras{}, err
	}
	hash, err := json.Marshal(hashesDoPadrao())
	if err != nil {
		return VersaoRegras{}, err
	}
	var restaurada any
	if restauradaDe != "" {
		restaurada = restauradaDe
	}
	v, err := scanVersaoRegras(tx.QueryRow(ctx,
		`INSERT INTO bot_regras_venda_versao (tenant_id, conteudo, padrao_hash, alteracoes, salvo_por, restaurada_de)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+colunasVersaoRegras,
		tenant, conteudo, hash, alteracoesEntre(anterior.Regras, doc), quem, restaurada))
	if err != nil {
		return VersaoRegras{}, fmt.Errorf("RegrasVendaRepo: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return VersaoRegras{}, fmt.Errorf("RegrasVendaRepo: commit: %w", err)
	}
	return v, nil
}

// VersaoResumo — uma linha do histórico da tela.
type VersaoResumo struct {
	ID           string    `json:"id"`
	Alteracoes   []string  `json:"alteracoes"`
	SalvoPor     string    `json:"salvoPor"`
	SalvoEm      time.Time `json:"salvoEm"`
	RestauradaDe string    `json:"restauradaDe,omitempty"`
}

// Versoes lista as últimas versões, da mais nova para a mais velha.
func (r *RegrasVendaRepo) Versoes(ctx context.Context, tenant TenantID, limite int) ([]VersaoResumo, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id::text, alteracoes, salvo_por, salvo_em, COALESCE(restaurada_de::text, '')
		 FROM bot_regras_venda_versao WHERE tenant_id = $1 ORDER BY seq DESC LIMIT $2`, tenant, limite)
	if err != nil {
		return nil, fmt.Errorf("RegrasVendaRepo.Versoes: %w", err)
	}
	defer rows.Close()
	out := []VersaoResumo{}
	for rows.Next() {
		var v VersaoResumo
		if err := rows.Scan(&v.ID, &v.Alteracoes, &v.SalvoPor, &v.SalvoEm, &v.RestauradaDe); err != nil {
			return nil, fmt.Errorf("RegrasVendaRepo.Versoes: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Versao devolve uma versão do tenant. Id malformado ou de outro tenant =
// ErrVersaoNaoEncontrada (não se distingue um do outro de propósito).
func (r *RegrasVendaRepo) Versao(ctx context.Context, tenant TenantID, id string) (VersaoRegras, error) {
	v, err := scanVersaoRegras(r.pool.QueryRow(ctx,
		`SELECT `+colunasVersaoRegras+` FROM bot_regras_venda_versao
		 WHERE tenant_id = $1 AND id::text = $2`, tenant, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return VersaoRegras{}, ErrVersaoNaoEncontrada
	}
	if err != nil {
		return VersaoRegras{}, fmt.Errorf("RegrasVendaRepo.Versao: %w", err)
	}
	return v, nil
}

// hashesDoPadrao — um resumo curto do texto padrão de cada parte.
func hashesDoPadrao() map[ParteRegra]string {
	p := PadraoRegrasVenda()
	out := map[ParteRegra]string{}
	for _, parte := range PartesDasRegras {
		out[parte] = hashTexto(p.Textos[parte])
	}
	return out
}

func hashTexto(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}

// alteracoesEntre diz o que mudou de uma versão gravada (sem padrão) para
// a próxima: "faixas", "vocabulario" e os nomes das partes.
func alteracoesEntre(antes, depois RegrasVenda) []string {
	out := []string{}
	if antes.Faixas != depois.Faixas {
		out = append(out, "faixas")
	}
	if !mesmoVocabulario(antes.Vocabulario, depois.Vocabulario) {
		out = append(out, "vocabulario")
	}
	for _, parte := range PartesDasRegras {
		if antes.Textos[parte] != depois.Textos[parte] {
			out = append(out, string(parte))
		}
	}
	return out
}
