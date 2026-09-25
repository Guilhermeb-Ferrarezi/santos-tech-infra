package main

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	agentdb "github.com/santos-tech/agent/db"
)

func tstz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// usagePeriod resume gasto, chamadas e tokens num intervalo (hoje, mês, total...).
type usagePeriod struct {
	CostUSD          float64 `json:"costUsd"`
	Calls            int64   `json:"calls"`
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	// APICostUSD é a parte que rodou com chave de API (custo REAL). O resto de
	// CostUSD é simulação: preço de tabela do que rodou na assinatura.
	APICostUSD float64 `json:"apiCostUsd"`
}

type usageDayPoint struct {
	Day     string  `json:"day"` // YYYY-MM-DD
	CostUSD float64 `json:"costUsd"`
	Calls   int64   `json:"calls"`
	Tokens  int64   `json:"tokens"` // input+output
}

type usageSourcePoint struct {
	Source  string  `json:"source"`
	CostUSD float64 `json:"costUsd"`
	Calls   int64   `json:"calls"`
	Tokens  int64   `json:"tokens"` // input+output
}

// usageTaskPoint quebra o gasto por task do /claude/generate (email, raw = bot do
// WhatsApp, diagram...). Só existe para chamadas one-shot — sessões interativas
// (source="session") não têm task e ficam de fora.
type usageTaskPoint struct {
	Task             string  `json:"task"`
	CostUSD          float64 `json:"costUsd"`
	Calls            int64   `json:"calls"`
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
}

// usageOriginPoint é o gasto por FUNÇÃO (quem pediu) nos últimos 30 dias —
// "bot", "bot-tarefas", "posaula", "sessao", "email"... "raw" = histórico de antes
// da separação por origem. week* = últimos 7 dias; api* = parte paga com chave.
type usageOriginPoint struct {
	Origin           string  `json:"origin"`
	CostUSD          float64 `json:"costUsd"`
	Calls            int64   `json:"calls"`
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	CacheReadTokens  int64   `json:"cacheReadTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	WeekCostUSD      float64 `json:"weekCostUsd"`
	WeekCalls        int64   `json:"weekCalls"`
	APICostUSD       float64 `json:"apiCostUsd"`
	APICalls         int64   `json:"apiCalls"`
}

type usageResponse struct {
	// Since é a data do primeiro registro — o "desde quando" do Total.
	Since  *string            `json:"since"`
	Total  usagePeriod        `json:"total"`
	Today  usagePeriod        `json:"today"`
	Week   usagePeriod        `json:"week"` // últimos 7 dias
	Month  usagePeriod        `json:"month"`
	Daily  []usageDayPoint    `json:"daily"`  // últimos 30 dias
	Source []usageSourcePoint `json:"source"` // últimos 30 dias, por origem (generate/generate_stream/session)
	Task   []usageTaskPoint   `json:"task"`   // últimos 30 dias, por task (bot=raw)
	Origin []usageOriginPoint `json:"origin"` // últimos 30 dias, por função (quem pediu)
}

// handleUsage devolve o resumo de gastos do CLI claude (agent-go) para o painel —
// custo e tokens hoje/mês/total e uma série diária dos últimos 30 dias. Admin-only.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	last30 := now.AddDate(0, 0, -30)
	last7 := now.AddDate(0, 0, -7)

	total, err := s.q.UsageSummary(ctx, tstz(epoch))
	if err != nil {
		writeErr(w, err)
		return
	}
	today, err := s.q.UsageSummary(ctx, tstz(startOfDay))
	if err != nil {
		writeErr(w, err)
		return
	}
	month, err := s.q.UsageSummary(ctx, tstz(startOfMonth))
	if err != nil {
		writeErr(w, err)
		return
	}
	week, err := s.q.UsageSummary(ctx, tstz(last7))
	if err != nil {
		writeErr(w, err)
		return
	}
	since, err := s.q.UsageSince(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	originRows, err := s.q.UsageByOrigin(ctx, agentdb.UsageByOriginParams{Since: tstz(last30), WeekSince: tstz(last7)})
	if err != nil {
		writeErr(w, err)
		return
	}
	dailyRows, err := s.q.UsageDaily(ctx, tstz(last30))
	if err != nil {
		writeErr(w, err)
		return
	}
	sourceRows, err := s.q.UsageBySource(ctx, tstz(last30))
	if err != nil {
		writeErr(w, err)
		return
	}
	taskRows, err := s.q.UsageByTask(ctx, tstz(last30))
	if err != nil {
		writeErr(w, err)
		return
	}

	toPeriod := func(row agentdb.UsageSummaryRow) usagePeriod {
		return usagePeriod{
			CostUSD:          row.TotalCostUsd,
			Calls:            row.Calls,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			APICostUSD:       row.ApiCostUsd,
		}
	}
	res := usageResponse{
		Since: tstzOrNil(since),
		Total: toPeriod(total),
		Today: toPeriod(today),
		Week:  toPeriod(week),
		Month: toPeriod(month),
	}
	res.Origin = make([]usageOriginPoint, 0, len(originRows))
	for _, row := range originRows {
		res.Origin = append(res.Origin, usageOriginPoint{
			Origin:           row.OriginKey,
			CostUSD:          row.CostUsd,
			Calls:            row.Calls,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			WeekCostUSD:      row.WeekCostUsd,
			WeekCalls:        row.WeekCalls,
			APICostUSD:       row.ApiCostUsd,
			APICalls:         row.ApiCalls,
		})
	}
	res.Daily = make([]usageDayPoint, 0, len(dailyRows))
	for _, row := range dailyRows {
		res.Daily = append(res.Daily, usageDayPoint{
			Day:     row.Day.Time.Format("2006-01-02"),
			CostUSD: row.CostUsd,
			Calls:   row.Calls,
			Tokens:  row.Tokens,
		})
	}
	res.Source = make([]usageSourcePoint, 0, len(sourceRows))
	for _, row := range sourceRows {
		res.Source = append(res.Source, usageSourcePoint{
			Source:  row.Source,
			CostUSD: row.CostUsd,
			Calls:   row.Calls,
			Tokens:  row.Tokens,
		})
	}
	res.Task = make([]usageTaskPoint, 0, len(taskRows))
	for _, row := range taskRows {
		res.Task = append(res.Task, usageTaskPoint{
			Task:             row.Task,
			CostUSD:          row.CostUsd,
			Calls:            row.Calls,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
		})
	}
	writeJSON(w, http.StatusOK, res)
}

// epoch é o início do intervalo pro total acumulado (sem limite inferior real).
var epoch = time.Unix(0, 0)
