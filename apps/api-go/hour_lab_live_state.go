package main

// Estado AO VIVO dos PCs do laboratório, no Redis.
//
// Cada PC manda heartbeat a cada 60s e, até aqui, todo heartbeat fazia um
// UPDATE na linha do dispositivo mesmo quando nada durável tinha mudado —
// porque `last_seen_at` sempre muda. Medido em produção em 03/09/2026, numa
// tabela de 13 linhas: 1577 updates, dos quais só 4,3% foram HOT. Ou seja, 96%
// reescreveram as quatro entradas de índice à toa, e o autovacuum já tinha
// rodado 29 vezes.
//
// Agora o que é volátil (visto por último, IP, uso de CPU/RAM/GPU e apps
// abertos) mora aqui, e o Postgres só é escrito quando muda algo durável — ou
// quando passa labLiveFloor desde a última escrita, pra o banco nunca ficar
// arbitrariamente desatualizado.
//
// FAIL-OPEN, igual ao resto do cache deste serviço (ver cache.go): sem Redis,
// nada quebra. A listagem continua lendo as colunas do Postgres, só com a
// granularidade do floor em vez de 60s.

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	labLivePrefix = "api-go:lab:live:"

	// TTL longo: aqui o TTL é só coleta de lixo de máquina que sumiu de vez,
	// não a definição de "online" (quem decide isso é o dashboard, comparando
	// lastSeenAt com o limiar de 90s). Curto demais faria um PC desligado há
	// uma hora voltar a mostrar o horário velho do Postgres.
	labLiveTTL = 7 * 24 * time.Hour

	// De quanto em quanto tempo o estado volátil é espelhado no Postgres mesmo
	// sem mudança durável. É o piso de frescor do banco caso o Redis suma, e o
	// que transforma 1440 escritas por dia por PC em ~144.
	labLiveFloor = 10 * time.Minute

	// Teto por operação no Redis — não pendura o heartbeat, que é a função
	// crítica da frota.
	labLiveOpTimeout = 2 * time.Second
)

// labLiveState é a foto volátil de um PC. Os campos espelham as colunas
// equivalentes do LabDevice; `omitempty` mantém a chave pequena pra um PC que
// só manda heartbeat, sem métrica nem lista de apps.
type labLiveState struct {
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	LastIP     string     `json:"lastIp,omitempty"`
	OpenApps   []string   `json:"openApps,omitempty"`
	OpenAppsAt *time.Time `json:"openAppsAt,omitempty"`
	CPUPercent *float64   `json:"cpuPercent,omitempty"`
	RAMPercent *float64   `json:"ramPercent,omitempty"`
	GPUPercent *float64   `json:"gpuPercent,omitempty"`
	GPUName    string     `json:"gpuName,omitempty"`
	MetricsAt  *time.Time `json:"metricsAt,omitempty"`
}

func labLiveKey(deviceUUID string) string { return labLivePrefix + deviceUUID }

// saveLabLiveState grava a foto volátil. Best-effort: erro só loga, porque o
// heartbeat já respondeu o que o PC precisa (comandos pendentes) e o Postgres
// recebe a mesma informação no próximo floor.
func (s *Server) saveLabLiveState(ctx context.Context, deviceUUID string, st labLiveState) {
	if s.rdb == nil {
		return
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return
	}
	opCtx, cancel := context.WithTimeout(ctx, labLiveOpTimeout)
	defer cancel()
	if err := s.rdb.Set(opCtx, labLiveKey(deviceUUID), raw, labLiveTTL).Err(); err != nil {
		slog.Warn("lab live state: falha ao gravar no Redis; o dashboard cai pro valor do Postgres até o próximo floor",
			"device_uuid", deviceUUID, "err", err)
	}
}

// applyLabLiveState sobrepõe, na lista vinda do Postgres, o que o Redis tem de
// mais fresco. Uma única ida ao Redis (MGET) pra página inteira — a listagem é
// chamada a cada 10s por admin com o dashboard aberto e não pode virar N+1.
//
// Fail-open: qualquer erro devolve a lista como veio do banco.
func (s *Server) applyLabLiveState(ctx context.Context, devices []LabDevice) {
	if s.rdb == nil || len(devices) == 0 {
		return
	}
	keys := make([]string, len(devices))
	for i, d := range devices {
		keys[i] = labLiveKey(d.DeviceUUID)
	}
	opCtx, cancel := context.WithTimeout(ctx, labLiveOpTimeout)
	defer cancel()
	vals, err := s.rdb.MGet(opCtx, keys...).Result()
	if err != nil && err != redis.Nil {
		slog.Warn("lab live state: falha ao ler do Redis; usando os valores do Postgres", "err", err)
		return
	}
	for i, v := range vals {
		if i >= len(devices) || v == nil {
			continue
		}
		raw, ok := v.(string)
		if !ok {
			continue
		}
		var st labLiveState
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			continue
		}
		st.overlay(&devices[i])
	}
}

// overlay aplica o estado do Redis sobre o registro do banco. Só sobrepõe o
// que existe: uma foto sem métrica (PC que ainda não reporta) não pode APAGAR
// a última métrica que o Postgres guardou.
func (st labLiveState) overlay(d *LabDevice) {
	if st.LastSeenAt != nil && (d.LastSeenAt == nil || st.LastSeenAt.After(*d.LastSeenAt)) {
		d.LastSeenAt = st.LastSeenAt
	}
	if st.LastIP != "" {
		ip := st.LastIP
		d.LastIP = &ip
	}
	if st.OpenAppsAt != nil {
		d.OpenApps = st.OpenApps
		if d.OpenApps == nil {
			d.OpenApps = []string{}
		}
		d.OpenAppsAt = st.OpenAppsAt
	}
	if st.CPUPercent != nil {
		d.CPUPercent = st.CPUPercent
	}
	if st.RAMPercent != nil {
		d.RAMPercent = st.RAMPercent
	}
	if st.GPUPercent != nil {
		d.GPUPercent = st.GPUPercent
	}
	if st.GPUName != "" {
		n := st.GPUName
		d.GPUName = &n
	}
	if st.MetricsAt != nil {
		d.MetricsAt = st.MetricsAt
	}
}

// liveStateFrom monta a foto volátil a partir do heartbeat recebido.
func liveStateFrom(hb labHeartbeat, agora time.Time) labLiveState {
	st := labLiveState{
		LastSeenAt: &agora,
		LastIP:     hb.IP,
		CPUPercent: hb.CPUPercent,
		RAMPercent: hb.RAMPercent,
		GPUPercent: hb.GPUPercent,
		GPUName:    hb.GPUName,
	}
	if hb.HasMetrics() {
		st.MetricsAt = &agora
	}
	// OpenApps ausente (app antigo) é diferente de lista vazia: só grava a
	// lista quando ela de fato veio, senão o overlay apagaria a última
	// conhecida — a mesma regra do CASE WHEN no SQL do upsert.
	if hb.OpenApps != nil {
		var apps []string
		if err := json.Unmarshal(hb.OpenApps, &apps); err == nil {
			st.OpenApps = apps
			st.OpenAppsAt = &agora
		}
	}
	return st
}
