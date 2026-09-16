package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AudioClipStore resolve intenções em arquivos de áudio pré-gravados.
//
// Por que existe: a voz do atendente foi clonada num plano de TTS que não será
// renovado. Não dá para sintetizar fala nova nessa voz — só existe o acervo já
// gerado. Este store é o que permite usar esse acervo por intenção, e registrar
// o que faltou para gravar depois.
//
// Os arquivos ficam em disco (AUDIO_CLIPS_DIR); o banco guarda só o índice.
type AudioClipStore struct {
	pool *pgxpool.Pool
	dir  string
}

func NewAudioClipStore(pool *pgxpool.Pool, dir string) *AudioClipStore {
	return &AudioClipStore{pool: pool, dir: dir}
}

// Enabled: sem diretório configurado, o store não opera (e o chamador cai no
// comportamento anterior). Fail-safe explícito em vez de erro no boot.
func (s *AudioClipStore) Enabled() bool {
	return s != nil && s.dir != "" && s.pool != nil
}

// AudioClip — uma variante gravada de uma intenção.
type AudioClip struct {
	IntentKey  string
	Variant    int
	FilePath   string // relativo a dir
	Transcript string
}

// Resolve devolve UMA variante ativa da intenção, sorteada.
//
// O sorteio é o que impede o cliente de perceber que está ouvindo gravação: a
// mesma saudação sai com palavras diferentes a cada vez. Sem variante ativa,
// devolve (nil, nil) — ausência não é erro, é o caso normal de lacuna.
func (s *AudioClipStore) Resolve(ctx context.Context, tenantID TenantID, voice, intentKey string) (*AudioClip, error) {
	if !s.Enabled() {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT intent_key, variant, file_path, transcript
		FROM audio_clips
		WHERE tenant_id = $1 AND voice = $2 AND intent_key = $3 AND active
	`, tenantID, voice, intentKey)
	if err != nil {
		return nil, fmt.Errorf("AudioClipStore.Resolve: %w", err)
	}
	defer rows.Close()

	var opts []AudioClip
	for rows.Next() {
		var c AudioClip
		if err := rows.Scan(&c.IntentKey, &c.Variant, &c.FilePath, &c.Transcript); err != nil {
			return nil, fmt.Errorf("AudioClipStore.Resolve scan: %w", err)
		}
		opts = append(opts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("AudioClipStore.Resolve rows: %w", err)
	}
	if len(opts) == 0 {
		return nil, nil
	}
	return &opts[rand.Intn(len(opts))], nil
}

// resolveClipPath transforma um file_path (vindo do banco) em caminho absoluto,
// recusando qualquer coisa que escape do diretório de áudios.
//
// Função pura e separada de propósito: é a fronteira de segurança do store e
// precisa ser testável sem banco nem disco montado. Um file_path como
// "../../etc/passwd" não pode virar leitura arbitrária dentro do container.
func resolveClipPath(dir, filePath string) (string, error) {
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolveClipPath abs dir: %w", err)
	}
	full, err := filepath.Abs(filepath.Join(base, filepath.FromSlash(filePath)))
	if err != nil {
		return "", fmt.Errorf("resolveClipPath abs file: %w", err)
	}
	if full != base && !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("resolveClipPath: caminho fora do diretório de áudios: %q", filePath)
	}
	return full, nil
}

// Read carrega os bytes do clipe do disco.
func (s *AudioClipStore) Read(clip *AudioClip) ([]byte, error) {
	if !s.Enabled() || clip == nil {
		return nil, fmt.Errorf("AudioClipStore.Read: store desabilitado ou clipe nulo")
	}
	return readClipFrom(s.dir, clip.FilePath)
}

// readClipFrom é o miolo do Read, sem depender do pool — o que permite testar
// leitura e validação de caminho sem Postgres.
func readClipFrom(dir, filePath string) ([]byte, error) {
	full, err := resolveClipPath(dir, filePath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("readClipFrom %s: %w", filePath, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("readClipFrom %s: arquivo vazio", filePath)
	}
	return data, nil
}

// RecordGap anota que o bot quis falar e não tinha clipe.
//
// Idempotente por (voz, intenção, texto): a mesma lacuna incrementa `hits`. É o
// que transforma a falta numa fila de gravação priorizada por frequência — as
// mais repetidas são as que valem gravar primeiro.
//
// Best-effort de propósito: falhar aqui não pode impedir o bot de responder.
func (s *AudioClipStore) RecordGap(ctx context.Context, tenantID TenantID, voice, intentKey, sampleText string) error {
	if !s.Enabled() {
		return nil
	}
	// Texto longo vira ruído no painel; o suficiente para reconhecer a fala.
	if len(sampleText) > 500 {
		sampleText = sampleText[:500]
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audio_gaps (tenant_id, voice, intent_key, sample_text)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, voice, intent_key, md5(sample_text))
		DO UPDATE SET hits = audio_gaps.hits + 1, last_seen = now(), resolved = false
	`, tenantID, voice, intentKey, sampleText)
	if err != nil {
		return fmt.Errorf("AudioClipStore.RecordGap: %w", err)
	}
	return nil
}

// AudioGapRow — uma lacuna, para listar no painel.
type AudioGapRow struct {
	ID         string `json:"id"`
	Voice      string `json:"voice"`
	IntentKey  string `json:"intentKey"`
	SampleText string `json:"sampleText"`
	Hits       int    `json:"hits"`
	Resolved   bool   `json:"resolved"`
	FirstSeen  string `json:"firstSeen"`
	LastSeen   string `json:"lastSeen"`
}

// ListGaps devolve as lacunas pendentes, mais frequentes primeiro — que é a
// ordem em que vale a pena gravar.
func (s *AudioClipStore) ListGaps(ctx context.Context, tenantID TenantID, incluirResolvidas bool, limit int) ([]AudioGapRow, error) {
	if !s.Enabled() {
		return []AudioGapRow{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, voice, intent_key, sample_text, hits, resolved,
		       to_char(first_seen, 'YYYY-MM-DD"T"HH24:MI:SSZ'),
		       to_char(last_seen,  'YYYY-MM-DD"T"HH24:MI:SSZ')
		FROM audio_gaps
		WHERE tenant_id = $1 AND ($2 OR NOT resolved)
		ORDER BY resolved, hits DESC, last_seen DESC
		LIMIT $3
	`, tenantID, incluirResolvidas, limit)
	if err != nil {
		return nil, fmt.Errorf("AudioClipStore.ListGaps: %w", err)
	}
	defer rows.Close()

	out := []AudioGapRow{}
	for rows.Next() {
		var g AudioGapRow
		if err := rows.Scan(&g.ID, &g.Voice, &g.IntentKey, &g.SampleText,
			&g.Hits, &g.Resolved, &g.FirstSeen, &g.LastSeen); err != nil {
			return nil, fmt.Errorf("AudioClipStore.ListGaps scan: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// clipManifest — o índice que viaja junto com os arquivos de áudio.
type clipManifest struct {
	Version int `json:"version"`
	Clips   []struct {
		Voice      string `json:"voice"`
		IntentKey  string `json:"intentKey"`
		Variant    int    `json:"variant"`
		FilePath   string `json:"filePath"`
		Transcript string `json:"transcript"`
		DurationMs int    `json:"durationMs"`
		Category   string `json:"category"`
	} `json:"clips"`
}

// SyncFromManifest lê `manifest.json` do diretório de áudios e sincroniza a
// tabela `audio_clips`.
//
// Roda no boot de propósito: assim o banco nunca fica dessincronizado do que
// está em disco, e não existe passo manual de importação para alguém esquecer.
// É idempotente (UPSERT por tenant+voz+intenção+variante), então rodar de novo
// a cada deploy é no-op quando nada mudou.
//
// Clipes que sumiram do manifesto viram `active=false` em vez de DELETE: o
// histórico de mensagens já enviadas continua fazendo sentido, e reverter um
// deploy não perde informação.
func (s *AudioClipStore) SyncFromManifest(ctx context.Context, tenantID TenantID) (int, error) {
	if !s.Enabled() {
		return 0, nil
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, "manifest.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // sem manifesto = nada a sincronizar; não é erro
		}
		return 0, fmt.Errorf("SyncFromManifest: ler manifesto: %w", err)
	}
	var man clipManifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return 0, fmt.Errorf("SyncFromManifest: json inválido: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("SyncFromManifest: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	vistos := make([]string, 0, len(man.Clips))
	for _, c := range man.Clips {
		if c.Voice == "" || c.IntentKey == "" || c.FilePath == "" {
			continue // linha malformada não derruba a sincronização inteira
		}
		// O caminho é validado aqui, no boot, e não na hora de enviar: um
		// manifesto adulterado falha alto em vez de virar leitura indevida
		// silenciosa no meio de um atendimento.
		if _, perr := resolveClipPath(s.dir, c.FilePath); perr != nil {
			return 0, fmt.Errorf("SyncFromManifest: %w", perr)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO audio_clips
			  (tenant_id, voice, intent_key, variant, file_path, transcript, duration_ms, category, active)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,true)
			ON CONFLICT (tenant_id, voice, intent_key, variant) DO UPDATE SET
			  file_path   = EXCLUDED.file_path,
			  transcript  = EXCLUDED.transcript,
			  duration_ms = EXCLUDED.duration_ms,
			  category    = EXCLUDED.category,
			  active      = true
		`, tenantID, c.Voice, c.IntentKey, c.Variant, c.FilePath, c.Transcript, c.DurationMs, c.Category)
		if err != nil {
			return 0, fmt.Errorf("SyncFromManifest: upsert %s/%s v%d: %w", c.Voice, c.IntentKey, c.Variant, err)
		}
		vistos = append(vistos, fmt.Sprintf("%s|%s|%d", c.Voice, c.IntentKey, c.Variant))
	}

	// Desativa o que não está mais no manifesto — mas NUNCA a partir de um
	// manifesto vazio. Um arquivo truncado ou mal gerado desativaria o banco
	// inteiro e o bot ficaria mudo sem ninguém entender por quê; é mais seguro
	// manter o índice anterior e falhar alto.
	if len(vistos) == 0 {
		return 0, fmt.Errorf("SyncFromManifest: manifesto sem clipes válidos; índice anterior preservado")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE audio_clips SET active = false
		WHERE tenant_id = $1
		  AND active
		  AND (voice || '|' || intent_key || '|' || variant::text) <> ALL($2::text[])
	`, tenantID, vistos); err != nil {
		return 0, fmt.Errorf("SyncFromManifest: desativar removidos: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("SyncFromManifest: commit: %w", err)
	}
	return len(vistos), nil
}

// ResolveGap marca uma lacuna como resolvida (áudio gravado e importado).
func (s *AudioClipStore) ResolveGap(ctx context.Context, tenantID TenantID, gapID string) error {
	if !s.Enabled() {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE audio_gaps SET resolved = true WHERE tenant_id = $1 AND id = $2::uuid
	`, tenantID, gapID)
	if err != nil {
		return fmt.Errorf("AudioClipStore.ResolveGap: %w", err)
	}
	return nil
}
