package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// "Onde este termo aparece" — para cada palavra da lista "evitar" das regras
// de venda, onde ela ainda está escrita.
//
// Em 25/09/2026 o Henrique viu o bot dizer "aulas para adultos" e o termo
// estava em três lugares: na Base de conhecimento, no código e em áudios
// gravados. Nenhuma tela mostrava isso ("como é que eu ia saber?"). Aqui a tela
// mostra. Quem corrige é gente — nada é trocado automaticamente.

// Fontes analisadas, na ordem da tela.
const (
	FonteBase          = "base"
	FontePromptSistema = "prompt_sistema"
	FontePromptAdmin   = "prompt_admin"
	FonteRegras        = "regras"
	FonteAudios        = "audios"
	FonteRespostas     = "respostas"
)

// diasDeRespostas — quantos dias de respostas do bot entram na busca.
const diasDeRespostas = 30

// maxRespostasAnalisadas — teto de mensagens lidas. A escola manda poucas
// centenas por mês; o teto só impede que a tela vire uma varredura sem fim.
const maxRespostasAnalisadas = 5000

type OcorrenciasFonte struct {
	Fonte    string   `json:"fonte"`
	Total    int      `json:"total"`
	Exemplos []string `json:"exemplos"`
}

type OcorrenciasTermo struct {
	Evitar string             `json:"evitar"`
	Total  int                `json:"total"`
	Fontes []OcorrenciasFonte `json:"fontes"`
}

type RespostaOcorrencias struct {
	Termos              []OcorrenciasTermo `json:"termos"`
	Dias                int                `json:"dias"`
	RespostasAnalisadas int                `json:"respostasAnalisadas"`
}

// ocorrenciasDoTermo conta quantas vezes o termo aparece no texto, sem ligar
// para acento nem maiúscula, e só como palavra inteira ("adulto" não casa
// dentro de "adultos"). Devolve um trecho do texto ORIGINAL em volta da
// primeira ocorrência.
func ocorrenciasDoTermo(texto, termo string) (int, string) {
	alvo := []rune(semAcento.Replace(strings.ToLower(strings.TrimSpace(termo))))
	if len(alvo) == 0 || texto == "" {
		return 0, ""
	}
	original := []rune(texto)
	norm := []rune(semAcento.Replace(strings.ToLower(texto)))
	// semAcento e ToLower trocam uma letra por uma letra; se algum caractere
	// raro mudar o tamanho, o trecho sai do texto normalizado.
	if len(norm) != len(original) {
		original = norm
	}
	letra := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

	n, primeira := 0, -1
	for i := 0; i+len(alvo) <= len(norm); i++ {
		if string(norm[i:i+len(alvo)]) != string(alvo) {
			continue
		}
		if i > 0 && letra(norm[i-1]) {
			continue
		}
		if fim := i + len(alvo); fim < len(norm) && letra(norm[fim]) {
			continue
		}
		if primeira < 0 {
			primeira = i
		}
		n++
		i += len(alvo) - 1
	}
	if n == 0 {
		return 0, ""
	}
	ini, fim := primeira-60, primeira+len(alvo)+60
	pre, pos := "…", "…"
	if ini <= 0 {
		ini, pre = 0, ""
	}
	if fim >= len(original) {
		fim, pos = len(original), ""
	}
	trecho := strings.Join(strings.Fields(string(original[ini:fim])), " ")
	return n, pre + trecho + pos
}

// textoRotulado — um texto de uma fonte, com um rótulo curto para o exemplo.
type textoRotulado struct {
	rotulo, texto string
}

// buscaOcorrencias lê as fontes do tenant e procura cada termo "evitar" das
// regras em vigor (vazio = lista padrão).
func buscaOcorrencias(ctx context.Context, pool *pgxpool.Pool, tenant TenantID, regras RegrasVenda) (RespostaOcorrencias, error) {
	fontes := map[string][]textoRotulado{}

	var kbRaw *string
	var systemPrompt, adminPrompt, botName, botGender string
	err := pool.QueryRow(ctx,
		`SELECT kb_content::text, system_prompt, admin_system_prompt, bot_name, bot_gender
		 FROM tenant_config WHERE tenant_id = $1`, tenant,
	).Scan(&kbRaw, &systemPrompt, &adminPrompt, &botName, &botGender)
	// Sem linha de config: não há Base nem prompt próprio — valem os padrões.
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return RespostaOcorrencias{}, fmt.Errorf("ocorrências: tenant_config: %w", err)
	}
	if kbRaw != nil {
		var entradas []KBEntry
		if json.Unmarshal([]byte(*kbRaw), &entradas) == nil {
			for _, e := range entradas {
				fontes[FonteBase] = append(fontes[FonteBase], textoRotulado{e.Title, e.Title + "\n" + e.Content})
			}
		}
	}
	// Prompt vazio = o bot usa o padrão do código; é esse que conta.
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = DefaultPersonaPrompt(TenantConfig{BotName: botName, BotGender: botGender}, ConversationContext{})
	}
	if strings.TrimSpace(adminPrompt) == "" {
		adminPrompt = DefaultAdminPrompt()
	}
	fontes[FontePromptSistema] = []textoRotulado{{"Prompt do sistema", systemPrompt}}
	fontes[FontePromptAdmin] = []textoRotulado{{"Prompt do admin", adminPrompt}}

	resolvidas := regras.Resolvida()
	for _, parte := range PartesDasRegras {
		fontes[FonteRegras] = append(fontes[FonteRegras], textoRotulado{string(parte), resolvidas.Textos[parte]})
	}

	rows, err := pool.Query(ctx,
		`SELECT intent_key, transcript FROM audio_clips WHERE tenant_id = $1 AND active AND transcript <> ''`, tenant)
	if err != nil {
		return RespostaOcorrencias{}, fmt.Errorf("ocorrências: audio_clips: %w", err)
	}
	for rows.Next() {
		var chave, transcricao string
		if err := rows.Scan(&chave, &transcricao); err != nil {
			rows.Close()
			return RespostaOcorrencias{}, fmt.Errorf("ocorrências: audio_clips: %w", err)
		}
		fontes[FonteAudios] = append(fontes[FonteAudios], textoRotulado{chave, transcricao})
	}
	rows.Close()

	rows, err = pool.Query(ctx,
		`SELECT coalesce(content->>'Text', content->>'text', '')
		 FROM outbound_message
		 WHERE tenant_id = $1 AND status <> 'failed' AND created_at > now() - make_interval(days => $2)
		 ORDER BY created_at DESC LIMIT $3`, tenant, diasDeRespostas, maxRespostasAnalisadas)
	if err != nil {
		return RespostaOcorrencias{}, fmt.Errorf("ocorrências: outbound_message: %w", err)
	}
	respostas := 0
	for rows.Next() {
		var texto string
		if err := rows.Scan(&texto); err != nil {
			rows.Close()
			return RespostaOcorrencias{}, fmt.Errorf("ocorrências: outbound_message: %w", err)
		}
		respostas++
		if texto != "" {
			fontes[FonteRespostas] = append(fontes[FonteRespostas], textoRotulado{"", texto})
		}
	}
	rows.Close()

	out := RespostaOcorrencias{Termos: []OcorrenciasTermo{}, Dias: diasDeRespostas, RespostasAnalisadas: respostas}
	for _, termo := range resolvidas.Vocabulario {
		tm := OcorrenciasTermo{Evitar: termo.Evitar, Fontes: []OcorrenciasFonte{}}
		for _, fonte := range []string{FonteBase, FontePromptSistema, FontePromptAdmin, FonteRegras, FonteAudios, FonteRespostas} {
			f := OcorrenciasFonte{Fonte: fonte, Exemplos: []string{}}
			for _, t := range fontes[fonte] {
				n, trecho := ocorrenciasDoTermo(t.texto, termo.Evitar)
				if n == 0 {
					continue
				}
				f.Total += n
				if len(f.Exemplos) < 3 {
					if t.rotulo != "" && fonte != FontePromptSistema && fonte != FontePromptAdmin {
						trecho = t.rotulo + ": " + trecho
					}
					f.Exemplos = append(f.Exemplos, trecho)
				}
			}
			tm.Total += f.Total
			tm.Fontes = append(tm.Fontes, f)
		}
		out.Termos = append(out.Termos, tm)
	}
	return out, nil
}

// GET /api/vendas/vocabulario/ocorrencias
func (s *Server) handleVendasOcorrencias(w http.ResponseWriter, r *http.Request) {
	tenant := s.tenantDoPainel()
	v, err := s.regrasVenda.Atual(r.Context(), tenant)
	if err != nil {
		s.logger.Error("vendas: ocorrências (regras)", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	res, err := buscaOcorrencias(r.Context(), s.pool, tenant, v.Regras)
	if err != nil {
		s.logger.Error("vendas: ocorrências", "err", err)
		jsonErr(w, "internal error", http.StatusInternalServerError)
		return
	}
	jsonOK(w, res)
}
