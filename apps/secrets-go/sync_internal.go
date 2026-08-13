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
	writeJSON(w, http.StatusOK, map[string]any{"hits": activeSyncHits(hits, syncHitsMaxSize)})
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
