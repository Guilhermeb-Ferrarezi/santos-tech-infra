package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
)

// refreshGraceTTL é a janela em que um refresh token JÁ rotacionado ainda é
// aceito como reenvio idempotente do mesmo request (retry de rede, ou uma
// segunda requisição concorrente que perdeu a corrida do DELETE atômico em
// consumeSessionByHash). Curta o suficiente para não virar uma janela de
// reuso útil a um token realmente roubado, longa o suficiente para cobrir
// qualquer retry/latência real de cliente.
const refreshGraceTTL = 30 * time.Second

func refreshGraceKey(oldHash string) string { return "api-go:refresh-grace:" + oldHash }

type cachedResponse struct {
	Status  int         `json:"status"`
	Headers http.Header `json:"headers"`
	Body    []byte      `json:"body"`
}

// respRecorder captura status/headers/body de uma resposta HTTP para poder
// tanto escrevê-la no ResponseWriter real quanto guardá-la em cache — sem
// reexecutar a lógica de negócio (que já rotacionou o token uma única vez).
type respRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newRespRecorder() *respRecorder {
	return &respRecorder{header: make(http.Header), status: http.StatusOK}
}

func (r *respRecorder) Header() http.Header         { return r.header }
func (r *respRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *respRecorder) WriteHeader(status int)      { r.status = status }

// flushTo escreve a resposta capturada no ResponseWriter real.
func (r *respRecorder) flushTo(w http.ResponseWriter) {
	dst := w.Header()
	for k, vs := range r.header {
		dst[k] = vs
	}
	w.WriteHeader(r.status)
	w.Write(r.body.Bytes())
}

// cacheGraceResponse guarda a resposta de uma rotação de refresh bem-sucedida,
// indexada pelo hash do refresh token QUE FOI CONSUMIDO (não o novo emitido).
// Um reenvio do mesmo token antigo dentro de refreshGraceTTL recebe essa MESMA
// resposta de volta (mesmos cookies/tokens) em vez de ser tratado como reuso
// malicioso — não é uma sessão nova nem uma segunda rotação.
func (s *Server) cacheGraceResponse(ctx context.Context, oldHash string, rec *respRecorder) {
	if s.rdb == nil {
		return
	}
	raw, err := json.Marshal(cachedResponse{Status: rec.status, Headers: rec.header, Body: rec.body.Bytes()})
	if err != nil {
		return
	}
	// Best-effort: se o Redis estiver indisponível, o pior caso é voltar ao
	// comportamento antigo (reenvio tratado como reuso suspeito) — não deve
	// derrubar a resposta original, que já foi computada com sucesso.
	_ = s.rdb.Set(ctx, refreshGraceKey(oldHash), raw, refreshGraceTTL).Err()
}

// tryGraceReplay tenta responder a partir de uma rotação idêntica já
// concluída dentro da janela de graça. Devolve true se respondeu (o caller
// não deve escrever mais nada).
func (s *Server) tryGraceReplay(ctx context.Context, w http.ResponseWriter, oldHash string) bool {
	if s.rdb == nil {
		return false
	}
	raw, err := s.rdb.Get(ctx, refreshGraceKey(oldHash)).Bytes()
	if err != nil {
		if err != redis.Nil {
			slog.Error("refresh_grace: falha ao consultar cache de replay", "err", err)
		}
		return false
	}
	var cached cachedResponse
	if err := json.Unmarshal(raw, &cached); err != nil {
		slog.Error("refresh_grace: cache de replay corrompido", "err", err)
		return false
	}
	dst := w.Header()
	for k, vs := range cached.Headers {
		dst[k] = vs
	}
	w.WriteHeader(cached.Status)
	w.Write(cached.Body)
	return true
}
