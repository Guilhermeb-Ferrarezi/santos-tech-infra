package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const brasilAPIFeriadosURL = "https://brasilapi.com.br/api/feriados/v1/"
const cacheFeriadosNacionaisTTL = 24 * time.Hour

// AgendaFeriado é o formato de saída unificado (nacional + municipal) —
// tipo diferencia a origem pro front, mas ambos aparecem juntos no calendário.
type AgendaFeriado struct {
	Data string `json:"data"`
	Nome string `json:"nome"`
	Tipo string `json:"tipo"` // "nacional" ou "municipal"
}

type brasilAPIFeriado struct {
	Date string `json:"date"`
	Name string `json:"name"`
}

// feriadosNacionaisClient busca feriados nacionais na BrasilAPI — pública,
// gratuita, sem chave. Mesmo shape de emailClient (email.go), sem header de
// auth e sem corpo de request (é um GET simples).
type feriadosNacionaisClient struct {
	client *http.Client
}

func newFeriadosNacionaisClient() *feriadosNacionaisClient {
	return &feriadosNacionaisClient{client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *feriadosNacionaisClient) buscar(ctx context.Context, ano int) ([]AgendaFeriado, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, brasilAPIFeriadosURL+strconv.Itoa(ano), nil)
	if err != nil {
		return nil, fmt.Errorf("criar request de feriados: %w", err)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("buscar feriados nacionais: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil, fmt.Errorf("brasilapi retornou status %d", res.StatusCode)
	}
	var raw []brasilAPIFeriado
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decodificar feriados: %w", err)
	}
	out := make([]AgendaFeriado, len(raw))
	for i, f := range raw {
		out[i] = AgendaFeriado{Data: f.Date, Nome: f.Name, Tipo: "nacional"}
	}
	return out, nil
}

func cacheFeriadosNacionaisKey(ano int) string {
	return "api-go:agenda:feriados-nacionais:" + strconv.Itoa(ano)
}

// listAgendaFeriados junta feriados nacionais (BrasilAPI, cacheados 24h via
// getOrSetJSON — cache.go) com os municipais cadastrados pelo admin.
// Fail-open: se a BrasilAPI/Redis falhar, devolve só os municipais em vez de
// quebrar a tela inteira (feriado é informativo, não deve derrubar a Agenda).
func (s *Server) listAgendaFeriados(ctx context.Context, ano int) ([]AgendaFeriado, error) {
	nacionais, err := getOrSetJSON(ctx, s.rdb, cacheFeriadosNacionaisKey(ano), cacheFeriadosNacionaisTTL,
		func(ctx context.Context) ([]AgendaFeriado, error) {
			return s.feriadosNacionais.buscar(ctx, ano)
		})
	if err != nil {
		nacionais = nil
	}
	municipais, err := s.listAgendaFeriadosMunicipaisAno(ctx, ano)
	if err != nil {
		return nil, err
	}
	return append(nacionais, municipais...), nil
}
