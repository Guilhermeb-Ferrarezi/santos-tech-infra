package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// Autorização do Google Agenda: duas rotas públicas, usadas uma vez por pessoa.
//
// São públicas porque a pessoa abre o link no celular, fora do painel. O que
// protege não é login: é o `state` de uso único, verificado no retorno. Sem
// ele, qualquer um que descobrisse a URL de callback poderia tentar plantar um
// código de autorização de outra conta.

// estadosPendentes guarda os `state` emitidos e ainda não usados.
//
// Em memória de propósito: são segundos de vida e um punhado de autorizações na
// vida do projeto. Um deploy no meio do fluxo invalida o state e a pessoa clica
// de novo — preço aceitável perto de criar tabela para isso.
type estadosPendentes struct {
	mu    sync.Mutex
	itens map[string]time.Time
}

var estadosGCal = &estadosPendentes{itens: map[string]time.Time{}}

func (e *estadosPendentes) novo() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	s := hex.EncodeToString(b)

	e.mu.Lock()
	defer e.mu.Unlock()
	// Limpa os vencidos aqui em vez de manter um timer: a limpeza acontece no
	// mesmo ritmo em que a estrutura é usada, que é raro.
	agora := time.Now()
	for k, exp := range e.itens {
		if agora.After(exp) {
			delete(e.itens, k)
		}
	}
	e.itens[s] = agora.Add(15 * time.Minute)
	return s
}

// consome valida e QUEIMA o state — um mesmo link não autoriza duas vezes.
func (e *estadosPendentes) consome(s string) bool {
	if s == "" {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, exp := range e.itens {
		// Comparação em tempo constante: o state é um segredo de uso único.
		if subtle.ConstantTimeCompare([]byte(k), []byte(s)) == 1 {
			delete(e.itens, k)
			return time.Now().Before(exp)
		}
	}
	return false
}

// handleGCalStart — GET /auth/google/start
// Abre a tela de consentimento do Google.
func (s *Server) handleGCalStart(w http.ResponseWriter, r *http.Request) {
	if s.gcal == nil || !s.gcal.Enabled() {
		http.Error(w, "Google Agenda não configurado neste servidor.", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, s.gcal.URLDeAutorizacao(estadosGCal.novo()), http.StatusFound)
}

// handleGCalCallback — GET /auth/google/callback
// O Google devolve o código aqui; trocamos por refresh token e guardamos.
func (s *Server) handleGCalCallback(w http.ResponseWriter, r *http.Request) {
	if s.gcal == nil || !s.gcal.Enabled() || s.gcalRepo == nil {
		http.Error(w, "Google Agenda não configurado neste servidor.", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	if erro := r.URL.Query().Get("error"); erro != "" {
		paginaGCal(w, http.StatusOK, "Autorização cancelada",
			"Você não autorizou o acesso à agenda. Se foi sem querer, abra o link de novo.")
		return
	}
	if !estadosGCal.consome(r.URL.Query().Get("state")) {
		// Link velho, já usado, ou vindo de outro lugar.
		paginaGCal(w, http.StatusBadRequest, "Link expirado",
			"Este link de autorização não vale mais. Peça um novo e tente outra vez.")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		paginaGCal(w, http.StatusBadRequest, "Faltou o código", "O Google não devolveu o código de autorização.")
		return
	}

	refresh, email, err := s.gcal.TrocaCodigo(ctx, code)
	if err != nil {
		s.logger.Error("gcal: falha ao trocar o código", "err", err)
		paginaGCal(w, http.StatusBadGateway, "Não deu certo",
			"Não consegui concluir a autorização. Tente de novo; se insistir, me avise.")
		return
	}
	if err := s.gcalRepo.Salvar(ctx, TenantID(s.cfg.TenantID), email, refresh); err != nil {
		s.logger.Error("gcal: falha ao salvar a conta", "err", err, "email", email)
		paginaGCal(w, http.StatusInternalServerError, "Não deu para salvar",
			"A autorização funcionou, mas não consegui guardar. Tente de novo.")
		return
	}

	s.logger.Info("gcal: conta autorizada", "email", email)
	paginaGCal(w, http.StatusOK, "Pronto!",
		"A agenda de "+email+" está conectada. As aulas experimentais marcadas pelo bot vão aparecer aí, com lembrete de 1 dia e 4 horas antes. Pode fechar esta página.")
}

// paginaGCal responde uma página simples — quem abre isso está no celular, não
// num painel, e merece uma frase em português em vez de JSON.
func paginaGCal(w http.ResponseWriter, status int, titulo, texto string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="pt-BR"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + titulo + `</title>
<style>
 body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;margin:0;
      min-height:100vh;display:grid;place-items:center;background:#0E2937;color:#F5F8FA}
 main{max-width:28rem;padding:2rem;text-align:center;line-height:1.6}
 h1{color:#0DB88F;font-size:1.5rem;margin:0 0 .75rem}
 p{margin:0;color:#c8d6de}
</style></head><body><main><h1>` + titulo + `</h1><p>` + texto + `</p></main></body></html>`))
}
