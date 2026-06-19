package main

import "time"

type AnalyticsRange struct {
	Key              string
	From, To         time.Time
	PrevFrom, PrevTo time.Time
	Bucket           string // "day" | "month"
}

type Bucket struct {
	Key   string `json:"key"`
	Total int64  `json:"total"`
	Count int64  `json:"count"`
}

type TimePoint struct {
	Date         string `json:"date"`
	PaidTotal    int64  `json:"paidTotal"`
	PaidCount    int64  `json:"paidCount"`
	EmittedTotal int64  `json:"emittedTotal"`
	EmittedCount int64  `json:"emittedCount"`
}

type AnalyticsKPIs struct {
	PaidTotal           int64   `json:"paidTotal"`
	PaidCount           int64   `json:"paidCount"`
	PendingTotal        int64   `json:"pendingTotal"`
	PendingCount        int64   `json:"pendingCount"`
	OverdueTotal        int64   `json:"overdueTotal"`
	OverdueCount        int64   `json:"overdueCount"`
	ExpiredCount        int64   `json:"expiredCount"`
	CanceledCount       int64   `json:"canceledCount"`
	AvgTicketCents      int64   `json:"avgTicketCents"`
	ConversionRate      float64 `json:"conversionRate"`
	ActiveSubscriptions int64   `json:"activeSubscriptions"`
	DeltaPaidPct        float64 `json:"deltaPaidPct"`
	emittedCount        int64
	prevPaidTotal       int64
}

type Analytics struct {
	Range       string        `json:"range"`
	KPIs        AnalyticsKPIs `json:"kpis"`
	Timeseries  []TimePoint   `json:"timeseries"`
	ByStatus    []Bucket      `json:"byStatus"`
	ByKind      []Bucket      `json:"byKind"`
	TopProducts []Bucket      `json:"topProducts"`
}

// parseRange traduz o parâmetro range para janelas de tempo e bucket. A janela
// "anterior" (mesmo tamanho, imediatamente antes) alimenta o deltaPaidPct.
func parseRange(s string) AnalyticsRange {
	now := time.Now()
	r := AnalyticsRange{Key: "30d", Bucket: "day"}
	var dur time.Duration
	switch s {
	case "7d":
		r.Key, dur = "7d", 7*24*time.Hour
	case "90d":
		r.Key, dur = "90d", 90*24*time.Hour
	case "12m":
		r.Key, r.Bucket = "12m", "month"
		r.From = now.AddDate(-1, 0, 0)
	case "all":
		r.Key, r.Bucket = "all", "month"
		r.From = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	default: // "" e "30d" e qualquer lixo
		r.Key, dur = "30d", 30*24*time.Hour
	}
	r.To = now
	if r.From.IsZero() {
		r.From = now.Add(-dur)
	}
	window := r.To.Sub(r.From)
	r.PrevTo = r.From
	r.PrevFrom = r.From.Add(-window)
	return r
}

// bucketDates devolve as datas (YYYY-MM-DD) de cada bucket no range, truncadas ao
// início do dia/mês — casando com o to_char(date_trunc(...)) do SQL do timeseries.
func bucketDates(r AnalyticsRange) []string {
	var out []string
	if r.Bucket == "month" {
		d := time.Date(r.From.Year(), r.From.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := time.Date(r.To.Year(), r.To.Month(), 1, 0, 0, 0, 0, time.UTC)
		for !d.After(end) {
			out = append(out, d.Format("2006-01-02"))
			d = d.AddDate(0, 1, 0)
		}
		return out
	}
	d := time.Date(r.From.Year(), r.From.Month(), r.From.Day(), 0, 0, 0, 0, time.UTC)
	end := time.Date(r.To.Year(), r.To.Month(), r.To.Day(), 0, 0, 0, 0, time.UTC)
	for !d.After(end) {
		out = append(out, d.Format("2006-01-02"))
		d = d.AddDate(0, 0, 1)
	}
	return out
}
