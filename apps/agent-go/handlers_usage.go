package main

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func tstz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// usagePeriod resume gasto e chamadas num intervalo (hoje, mês, total...).
type usagePeriod struct {
	CostUSD float64 `json:"costUsd"`
	Calls   int64   `json:"calls"`
}

type usageDayPoint struct {
	Day     string  `json:"day"` // YYYY-MM-DD
	CostUSD float64 `json:"costUsd"`
	Calls   int64   `json:"calls"`
}

type usageSourcePoint struct {
	Source  string  `json:"source"`
	CostUSD float64 `json:"costUsd"`
	Calls   int64   `json:"calls"`
}

type usageResponse struct {
	Total  usagePeriod        `json:"total"`
	Today  usagePeriod        `json:"today"`
	Month  usagePeriod        `json:"month"`
	Daily  []usageDayPoint    `json:"daily"`  // últimos 30 dias
	Source []usageSourcePoint `json:"source"` // últimos 30 dias, por origem (generate/generate_stream/session)
}

// handleUsage devolve o resumo de gastos do CLI claude (agent-go) para o painel —
// custo hoje/mês/total e uma série diária dos últimos 30 dias. Admin-only.
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

	res := usageResponse{
		Total: usagePeriod{CostUSD: total.TotalCostUsd, Calls: total.Calls},
		Today: usagePeriod{CostUSD: today.TotalCostUsd, Calls: today.Calls},
		Month: usagePeriod{CostUSD: month.TotalCostUsd, Calls: month.Calls},
	}
	res.Daily = make([]usageDayPoint, 0, len(dailyRows))
	for _, row := range dailyRows {
		res.Daily = append(res.Daily, usageDayPoint{
			Day:     row.Day.Time.Format("2006-01-02"),
			CostUSD: row.CostUsd,
			Calls:   row.Calls,
		})
	}
	res.Source = make([]usageSourcePoint, 0, len(sourceRows))
	for _, row := range sourceRows {
		res.Source = append(res.Source, usageSourcePoint{
			Source:  row.Source,
			CostUSD: row.CostUsd,
			Calls:   row.Calls,
		})
	}
	writeJSON(w, http.StatusOK, res)
}

// epoch é o início do intervalo pro total acumulado (sem limite inferior real).
var epoch = time.Unix(0, 0)
