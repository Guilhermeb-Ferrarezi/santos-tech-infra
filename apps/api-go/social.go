package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type SocialPost struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Caption      string     `json:"caption"`
	Platform     string     `json:"platform"`
	Pilar        string     `json:"pilar"`
	Status       string     `json:"status"`
	ScheduledAt  *time.Time `json:"scheduledAt"`
	MediaURL     string     `json:"mediaUrl"`
	ReferenceURL string     `json:"referenceUrl"`

	// Mídia via Arquivos (Google Drive) em vez de/além de MediaURL colada à
	// mão — DriveFolderID é o id de QUALQUER pasta cadastrada em drive_folders
	// (múltiplas pastas, não uma fixa), DriveFileID o arquivo escolhido dentro
	// dela. DriveFileName é só cache de exibição (evita nova chamada ao Drive
	// só pra mostrar o nome no card do post). Ver social_publish.go.
	DriveFolderID *string `json:"driveFolderId"`
	DriveFileID   string  `json:"driveFileId"`
	DriveFileName string  `json:"driveFileName"`

	// Capa customizada de Reel — opcional, mesmo shape do trio acima
	// (ver resolveSocialPostCoverURL em social_publish.go). Vazio = a Meta
	// escolhe o frame 0 do vídeo como capa.
	DriveCoverFolderID *string `json:"driveCoverFolderId"`
	DriveCoverFileID   string  `json:"driveCoverFileId"`
	DriveCoverFileName string  `json:"driveCoverFileName"`

	// Texto alternativo de acessibilidade — só usado em imagem estática
	// (Instagram alt_text / Facebook alt_text_custom).
	AltText string `json:"altText"`

	// Itens 2..10 de um carrossel (array de {folderId,fileId,fileName} em
	// JSON) — o item 1 é o trio DriveFolderID/DriveFileID/DriveFileName
	// acima. Só relevante quando Formato == "carrossel"; ver
	// parseCarouselItems em social_publish.go.
	CarouselItems json.RawMessage `json:"carouselItems"`

	Formato            string          `json:"formato"`
	Objetivo           string          `json:"objetivo"`
	Programa           string          `json:"programa"`
	Receita            string          `json:"receita"`
	PlataformasDestino []string        `json:"plataformasDestino"`
	CopyArte           json.RawMessage `json:"copyArte"`
	Hashtags           []string        `json:"hashtags"`
	ConceitoVisual     string          `json:"conceitoVisual"`
	Paleta             json.RawMessage `json:"paleta"`
	// Roteiro de fala — texto corrido único por post, só usado em formato de
	// vídeo (reel/short/video_longo). Prompt de IA agora é por slide, dentro
	// de CopyArte (json.RawMessage, opaco aqui — ver CopyArteSlide no
	// dashboard).
	Roteiro       string          `json:"roteiro"`
	Specs         json.RawMessage `json:"specs"`
	MasterURL     string          `json:"masterUrl"`
	Mandatorios   string          `json:"mandatorios"`
	ResponsavelID *int64          `json:"responsavelId"`
	// Justificativa obrigatória quando Status=="sem_recurso" (ver
	// validateSocialPostInput) — o que falta pra produzir a peça (aluno, turma,
	// sala etc.). Vazia pra qualquer outro status.
	MotivoSemRecurso string `json:"motivoSemRecurso"`
	FunilEtapa       string `json:"funilEtapa"`
	// Série (linha de conteúdo, ex.: "Tela&Saúde") — nil quando o post não
	// está associado a nenhuma. Só leitura; ver SocialPostInput.SerieID.
	Serie *SocialSerieRef `json:"serie"`
	// Conta (de quem é a rede social do post, ex.: "Santos Tech", "Edson") —
	// SEM ponteiro, sempre presente (diferente de Serie, que é opcional): todo
	// post tem exatamente 1 conta. Só leitura; ver SocialPostInput.ContaID.
	Conta SocialContaRef `json:"conta"`

	ResponsavelNome string `json:"responsavelNome"`
	// AssigneeIDs são responsáveis ADICIONAIS além de ResponsavelID (o
	// principal), mesma convenção de Task.AssigneeIDs em task.go.
	AssigneeIDs []int64   `json:"assigneeIds"`
	CreatedBy   *int64    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`

	// Só populado por handleGetSocialPost (post único) — listSocialPosts não
	// busca isso pra não pagar N+1 numa tela com dezenas de posts; o checklist
	// só é usado dentro do diálogo de edição de 1 post por vez.
	PublishConfirmations []SocialPostPublishConfirmation `json:"publishConfirmations"`
}

// SocialPostPublishConfirmation: 1 linha = "essa plataforma recebeu essa peça",
// confirmado por alguém. ConfirmedByID vem sempre da sessão autenticada no
// handler — nunca de um valor mandado pelo cliente, pra não dar pra fraudar
// "quem confirmou".
type SocialPostPublishConfirmation struct {
	Platform      string    `json:"platform"`
	ConfirmedByID *int64    `json:"confirmedById"`
	ConfirmedBy   string    `json:"confirmedByName"`
	ConfirmedAt   time.Time `json:"confirmedAt"`
}

// SocialPlatformOwner: mapeamento global (não por post) de quem pode
// confirmar/desconfirmar aquele canal no checklist de publicação.
type SocialPlatformOwner struct {
	Platform  string    `json:"platform"`
	UserID    int64     `json:"userId"`
	UserName  string    `json:"userName"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type SocialPostNote struct {
	ID         int64     `json:"id"`
	PostID     string    `json:"postId"`
	AuthorID   *int64    `json:"authorId"`
	AuthorName string    `json:"authorName"`
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"createdAt"`
}

type SocialPostInput struct {
	Title        string     `json:"title"`
	Caption      string     `json:"caption"`
	Platform     string     `json:"platform"`
	Pilar        string     `json:"pilar"`
	Status       string     `json:"status"`
	ScheduledAt  *time.Time `json:"scheduledAt"`
	MediaURL     string     `json:"mediaUrl"`
	ReferenceURL string     `json:"referenceUrl"`

	DriveFolderID *string `json:"driveFolderId"`
	DriveFileID   string  `json:"driveFileId"`
	DriveFileName string  `json:"driveFileName"`

	DriveCoverFolderID *string `json:"driveCoverFolderId"`
	DriveCoverFileID   string  `json:"driveCoverFileId"`
	DriveCoverFileName string  `json:"driveCoverFileName"`

	AltText       string          `json:"altText"`
	CarouselItems json.RawMessage `json:"carouselItems"`

	Formato            string          `json:"formato"`
	Objetivo           string          `json:"objetivo"`
	Programa           string          `json:"programa"`
	Receita            string          `json:"receita"`
	PlataformasDestino []string        `json:"plataformasDestino"`
	CopyArte           json.RawMessage `json:"copyArte"`
	Hashtags           []string        `json:"hashtags"`
	ConceitoVisual     string          `json:"conceitoVisual"`
	Paleta             json.RawMessage `json:"paleta"`
	Roteiro            string          `json:"roteiro"`
	Specs              json.RawMessage `json:"specs"`
	MasterURL          string          `json:"masterUrl"`
	Mandatorios        string          `json:"mandatorios"`
	ResponsavelID      *int64          `json:"responsavelId"`
	MotivoSemRecurso   string          `json:"motivoSemRecurso"`
	FunilEtapa         string          `json:"funilEtapa"`
	AssigneeIDs        []int64         `json:"assigneeIds"`
	// SerieID referencia social_series(id); nil = sem série. Validado em
	// validateSerieID (existência) antes de INSERT/UPDATE.
	SerieID *int64 `json:"serieId"`
	// ContaID referencia social_contas(id); nil = ainda não resolvido — o
	// handler substitui pelo id da conta "Santos Tech" antes de validar e
	// gravar (toda post tem exatamente 1 conta). Validado em validateContaID
	// (existência) antes de INSERT/UPDATE.
	ContaID *int64 `json:"contaId"`
}

// SocialSerie: uma linha de conteúdo do calendário editorial (ex.:
// "Tela&Saúde", "GMN") — lista fechada, só admin cadastra/edita/desativa
// (GET/POST/PUT /social/series). Ativa=false só esconde do dropdown de
// criação; posts que já usam a série continuam mostrando o nome normalmente.
type SocialSerie struct {
	ID        int64     `json:"id"`
	Nome      string    `json:"nome"`
	Ativa     bool      `json:"ativa"`
	CreatedAt time.Time `json:"createdAt"`
}

// SocialSerieRef é a projeção mínima (id+nome) embutida em SocialPost.Serie.
type SocialSerieRef struct {
	ID   int64  `json:"id"`
	Nome string `json:"nome"`
}

// SocialConta: de quem é a rede social do post (ex.: "Santos Tech", "Edson")
// — lista fechada, só admin cadastra/edita/desativa (GET/POST/PUT
// /social/contas). Diferente de SocialSerie: obrigatória (todo post tem
// exatamente 1) e nasce com seed inicial (Santos Tech + Edson) na migração.
// Ativa=false só esconde do dropdown de criação — posts que já usam a conta
// continuam mostrando o nome normalmente.
type SocialConta struct {
	ID        int64     `json:"id"`
	Nome      string    `json:"nome"`
	Ativa     bool      `json:"ativa"`
	CreatedAt time.Time `json:"createdAt"`
}

// SocialContaRef é a projeção mínima (id+nome) embutida em SocialPost.Conta.
type SocialContaRef struct {
	ID   int64  `json:"id"`
	Nome string `json:"nome"`
}

// defaultSocialContaNome é a conta usada quando o post não informa contaId
// explicitamente (ver resolveDefaultContaID). Seedada pela migração.
const defaultSocialContaNome = "Santos Tech"

// socialPostInputFromCurrent copia os campos editáveis do post atual para um
// SocialPostInput — é a base sobre a qual o PUT parcial (mergeSocialPostInput)
// aplica só os campos presentes no corpo da requisição.
func socialPostInputFromCurrent(p *SocialPost) SocialPostInput {
	return SocialPostInput{
		Title:              p.Title,
		Caption:            p.Caption,
		Platform:           p.Platform,
		Pilar:              p.Pilar,
		Status:             p.Status,
		ScheduledAt:        p.ScheduledAt,
		MediaURL:           p.MediaURL,
		ReferenceURL:       p.ReferenceURL,
		DriveFolderID:      p.DriveFolderID,
		DriveFileID:        p.DriveFileID,
		DriveFileName:      p.DriveFileName,
		DriveCoverFolderID: p.DriveCoverFolderID,
		DriveCoverFileID:   p.DriveCoverFileID,
		DriveCoverFileName: p.DriveCoverFileName,
		AltText:            p.AltText,
		CarouselItems:      p.CarouselItems,
		Formato:            p.Formato,
		Objetivo:           p.Objetivo,
		Programa:           p.Programa,
		Receita:            p.Receita,
		PlataformasDestino: p.PlataformasDestino,
		CopyArte:           p.CopyArte,
		Hashtags:           p.Hashtags,
		ConceitoVisual:     p.ConceitoVisual,
		Paleta:             p.Paleta,
		Roteiro:            p.Roteiro,
		Specs:              p.Specs,
		MasterURL:          p.MasterURL,
		Mandatorios:        p.Mandatorios,
		ResponsavelID:      p.ResponsavelID,
		MotivoSemRecurso:   p.MotivoSemRecurso,
		FunilEtapa:         p.FunilEtapa,
		AssigneeIDs:        p.AssigneeIDs,
		SerieID:            serieIDOf(p.Serie),
		ContaID:            &p.Conta.ID,
	}
}

// serieIDOf extrai o id da referência de série embutida em SocialPost —
// usado só para reidratar o SocialPostInput a partir do post atual (PUT
// parcial); nil se o post não tem série.
func serieIDOf(s *SocialSerieRef) *int64 {
	if s == nil {
		return nil
	}
	id := s.ID
	return &id
}

// mergeSocialPostInput aplica em `in` só os campos presentes em `raw` (chave
// JSON → ponteiro do campo correspondente). Um campo ausente do JSON não é
// tocado (o handler já preencheu `in` com socialPostInputFromCurrent); um
// campo presente — mesmo com valor vazio ("", [], null) — é uma alteração
// intencional e sobrescreve o valor atual. É o que faz o PUT /social/posts/{id}
// ser uma atualização PARCIAL em vez de um replace do objeto inteiro.
func mergeSocialPostInput(in *SocialPostInput, raw map[string]json.RawMessage) error {
	fields := map[string]any{
		"title":              &in.Title,
		"caption":            &in.Caption,
		"platform":           &in.Platform,
		"pilar":              &in.Pilar,
		"status":             &in.Status,
		"scheduledAt":        &in.ScheduledAt,
		"mediaUrl":           &in.MediaURL,
		"referenceUrl":       &in.ReferenceURL,
		"driveFolderId":      &in.DriveFolderID,
		"driveFileId":        &in.DriveFileID,
		"driveFileName":      &in.DriveFileName,
		"driveCoverFolderId": &in.DriveCoverFolderID,
		"driveCoverFileId":   &in.DriveCoverFileID,
		"driveCoverFileName": &in.DriveCoverFileName,
		"altText":            &in.AltText,
		"carouselItems":      &in.CarouselItems,
		"formato":            &in.Formato,
		"objetivo":           &in.Objetivo,
		"programa":           &in.Programa,
		"receita":            &in.Receita,
		"plataformasDestino": &in.PlataformasDestino,
		"copyArte":           &in.CopyArte,
		"hashtags":           &in.Hashtags,
		"conceitoVisual":     &in.ConceitoVisual,
		"paleta":             &in.Paleta,
		"roteiro":            &in.Roteiro,
		"specs":              &in.Specs,
		"masterUrl":          &in.MasterURL,
		"mandatorios":        &in.Mandatorios,
		"responsavelId":      &in.ResponsavelID,
		"motivoSemRecurso":   &in.MotivoSemRecurso,
		"funilEtapa":         &in.FunilEtapa,
		"assigneeIds":        &in.AssigneeIDs,
		"serieId":            &in.SerieID,
		"contaId":            &in.ContaID,
	}
	for key, ptr := range fields {
		v, ok := raw[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(v, ptr); err != nil {
			return fmt.Errorf("campo %q: %w", key, err)
		}
	}
	return nil
}

var validSocialPlatforms = map[string]bool{
	"facebook": true, "instagram": true, "tiktok": true, "youtube": true,
	"threads": true, "google_meu_negocio": true, "blog": true, "twitter_x": true, "linkedin": true,
}

var validSocialPilares = map[string]bool{
	"educacional": true, "institucional": true, "captacao": true,
	"prova_social": true, "bastidores": true, "tech_mundo_real": true,
}

var validSocialStatuses = map[string]bool{
	"ideia": true, "planejado": true, "em_producao": true,
	"revisao": true, "aprovado": true, "agendado": true, "publicado": true, "arquivado": true,
	"sem_recurso": true,
}

var validSocialFormatos = map[string]bool{
	"estatico": true, "carrossel": true, "video_curto": true, "story": true,
	"video_longo": true, "thumbnail": true, "card_link": true,
}
var validSocialObjetivos = map[string]bool{
	"alcance": true, "engajamento": true, "conversao": true, "autoridade": true,
}
var validSocialProgramas = map[string]bool{
	"": true, "create": true, "jr": true, "camps": true, "academies": true,
}

// Etapa do funil de tráfego pago. Vazio = peça sem função definida no funil
// (é o default das peças antigas, criadas antes deste campo existir).
var validSocialFunilEtapas = map[string]bool{
	"": true, "topo": true, "meio": true, "fundo": true,
}

var validSocialReceitas = map[string]bool{
	"": true, "capa_gancho": true, "hero_numero": true, "versus": true,
	"antes_depois": true, "desenvolvimento": true, "cta_fechamento": true,
	"checklist": true, "passo_a_passo": true, "citacao_depoimento": true, "poster_anuncio": true,
	// Vocabulário específico do Edson (cliente externo) — ver PENDENCIAS.md.
	// "templatezao": fundo branco, frase, foto de perfil, @ verificado, só
	// troca o texto. "ilustrativo": arte com conceito próprio gerada por IA.
	"templatezao": true, "ilustrativo": true,
}

const socialPostCols = `id::text, title, caption, platform, pilar, status,
	scheduled_at, media_url, reference_url, drive_folder_id::text, drive_file_id, drive_file_name,
	drive_cover_folder_id::text, drive_cover_file_id, drive_cover_file_name, alt_text, carousel_items,
	formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
	conceito_visual, paleta, roteiro, specs, master_url, mandatorios,
	responsavel_id, motivo_sem_recurso, funil_etapa, COALESCE((SELECT name FROM users WHERE id = responsavel_id), ''),
	COALESCE((SELECT array_agg(sa.user_id ORDER BY sa.added_at) FROM social_post_assignees sa WHERE sa.post_id = social_posts.id), '{}'),
	serie_id, (SELECT nome FROM social_series WHERE id = social_posts.serie_id),
	conta_id, (SELECT nome FROM social_contas WHERE id = social_posts.conta_id),
	created_by, created_at, updated_at`

func scanSocialPost(row pgx.Row) (*SocialPost, error) {
	var p SocialPost
	var serieID *int64
	var serieNome *string
	var contaNome string
	err := row.Scan(&p.ID, &p.Title, &p.Caption, &p.Platform, &p.Pilar, &p.Status,
		&p.ScheduledAt, &p.MediaURL, &p.ReferenceURL, &p.DriveFolderID, &p.DriveFileID, &p.DriveFileName,
		&p.DriveCoverFolderID, &p.DriveCoverFileID, &p.DriveCoverFileName, &p.AltText, &p.CarouselItems,
		&p.Formato, &p.Objetivo, &p.Programa, &p.Receita, &p.PlataformasDestino, &p.CopyArte, &p.Hashtags,
		&p.ConceitoVisual, &p.Paleta, &p.Roteiro, &p.Specs, &p.MasterURL, &p.Mandatorios,
		&p.ResponsavelID, &p.MotivoSemRecurso, &p.FunilEtapa, &p.ResponsavelNome, &p.AssigneeIDs,
		&serieID, &serieNome,
		&p.Conta.ID, &contaNome,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if serieID != nil {
		nome := ""
		if serieNome != nil {
			nome = *serieNome
		}
		p.Serie = &SocialSerieRef{ID: *serieID, Nome: nome}
	}
	p.Conta.Nome = contaNome
	return &p, nil
}

func jsonbOrDefault(raw json.RawMessage, def string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(def)
	}
	return raw
}
func sliceOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) listSocialPosts(ctx context.Context) ([]SocialPost, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+socialPostCols+` FROM social_posts ORDER BY COALESCE(scheduled_at, created_at) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialPost{}
	for rows.Next() {
		p, err := scanSocialPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Server) getSocialPost(ctx context.Context, id string) (*SocialPost, error) {
	return scanSocialPost(s.db.QueryRow(ctx,
		`SELECT `+socialPostCols+` FROM social_posts WHERE id = $1::uuid`, id))
}

// replaceSocialPostAssignees espelha replaceTaskAssignees (task.go) para
// social_post_assignees — mesmo contrato "manda a lista completa".
func replaceSocialPostAssignees(ctx context.Context, tx pgx.Tx, postID string, userIDs []int64, addedBy int64) error {
	if _, err := tx.Exec(ctx, `DELETE FROM social_post_assignees WHERE post_id=$1::uuid`, postID); err != nil {
		return err
	}
	if len(userIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO social_post_assignees (post_id, user_id, added_by)
		SELECT $1::uuid, uid, $3 FROM unnest($2::bigint[]) AS uid
		ON CONFLICT (post_id, user_id) DO NOTHING`,
		postID, userIDs, addedBy)
	return err
}

func (s *Server) insertSocialPost(ctx context.Context, in SocialPostInput, createdBy int64) (*SocialPost, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO social_posts (title, caption, platform, pilar, status, scheduled_at, media_url, reference_url,
			drive_folder_id, drive_file_id, drive_file_name,
			drive_cover_folder_id, drive_cover_file_id, drive_cover_file_name, alt_text, carousel_items,
			formato, objetivo, programa, receita, plataformas_destino, copy_arte, hashtags,
			conceito_visual, paleta, roteiro, specs, master_url, mandatorios, responsavel_id, motivo_sem_recurso, funil_etapa, serie_id, conta_id, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::uuid,$10,$11,$12::uuid,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35)
		RETURNING id::text`,
		in.Title, in.Caption, in.Platform, in.Pilar, in.Status, in.ScheduledAt, in.MediaURL, in.ReferenceURL,
		in.DriveFolderID, in.DriveFileID, in.DriveFileName,
		in.DriveCoverFolderID, in.DriveCoverFileID, in.DriveCoverFileName,
		in.AltText, jsonbOrDefault(in.CarouselItems, "[]"),
		in.Formato, in.Objetivo, in.Programa, in.Receita, sliceOrEmpty(in.PlataformasDestino),
		jsonbOrDefault(in.CopyArte, "[]"), sliceOrEmpty(in.Hashtags),
		in.ConceitoVisual, jsonbOrDefault(in.Paleta, "{}"), in.Roteiro, jsonbOrDefault(in.Specs, "{}"),
		in.MasterURL, in.Mandatorios, in.ResponsavelID, in.MotivoSemRecurso, in.FunilEtapa, in.SerieID, in.ContaID, createdBy).Scan(&id)
	if err != nil {
		return nil, portalDBErr(err)
	}
	if err := replaceSocialPostAssignees(ctx, tx, id, in.AssigneeIDs, createdBy); err != nil {
		return nil, portalDBErr(err)
	}
	post, err := scanSocialPost(tx.QueryRow(ctx, `SELECT `+socialPostCols+` FROM social_posts WHERE id=$1::uuid`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return post, nil
}

func (s *Server) updateSocialPost(ctx context.Context, id string, in SocialPostInput, updatedBy int64) (*SocialPost, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE social_posts SET
			title=$2, caption=$3, platform=$4, pilar=$5, status=$6, scheduled_at=$7, media_url=$8, reference_url=$9,
			drive_folder_id=$10::uuid, drive_file_id=$11, drive_file_name=$12,
			drive_cover_folder_id=$13::uuid, drive_cover_file_id=$14, drive_cover_file_name=$15, alt_text=$16, carousel_items=$17,
			formato=$18, objetivo=$19, programa=$20, receita=$21, plataformas_destino=$22, copy_arte=$23, hashtags=$24,
			conceito_visual=$25, paleta=$26, roteiro=$27, specs=$28, master_url=$29, mandatorios=$30, responsavel_id=$31, motivo_sem_recurso=$32, funil_etapa=$33, serie_id=$34, conta_id=$35, updated_at=now()
		WHERE id=$1::uuid`,
		id, in.Title, in.Caption, in.Platform, in.Pilar, in.Status, in.ScheduledAt, in.MediaURL, in.ReferenceURL,
		in.DriveFolderID, in.DriveFileID, in.DriveFileName,
		in.DriveCoverFolderID, in.DriveCoverFileID, in.DriveCoverFileName,
		in.AltText, jsonbOrDefault(in.CarouselItems, "[]"),
		in.Formato, in.Objetivo, in.Programa, in.Receita, sliceOrEmpty(in.PlataformasDestino),
		jsonbOrDefault(in.CopyArte, "[]"), sliceOrEmpty(in.Hashtags),
		in.ConceitoVisual, jsonbOrDefault(in.Paleta, "{}"), in.Roteiro, jsonbOrDefault(in.Specs, "{}"),
		in.MasterURL, in.Mandatorios, in.ResponsavelID, in.MotivoSemRecurso, in.FunilEtapa, in.SerieID, in.ContaID); err != nil {
		return nil, portalDBErr(err)
	}
	if err := replaceSocialPostAssignees(ctx, tx, id, in.AssigneeIDs, updatedBy); err != nil {
		return nil, portalDBErr(err)
	}
	post, err := scanSocialPost(tx.QueryRow(ctx, `SELECT `+socialPostCols+` FROM social_posts WHERE id=$1::uuid`, id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return post, nil
}

// updateSocialPostStatus atualiza o status e, opcionalmente, o motivo (nil =
// não mexe no que já está salvo — ver handleUpdateSocialPostStatus).
func (s *Server) updateSocialPostStatus(ctx context.Context, id, status string, motivoSemRecurso *string) (*SocialPost, error) {
	if motivoSemRecurso != nil {
		return scanSocialPost(s.db.QueryRow(ctx, `
			UPDATE social_posts SET status=$2, motivo_sem_recurso=$3, updated_at=now()
			WHERE id=$1::uuid RETURNING `+socialPostCols, id, status, *motivoSemRecurso))
	}
	return scanSocialPost(s.db.QueryRow(ctx, `
		UPDATE social_posts SET status=$2, updated_at=now()
		WHERE id=$1::uuid RETURNING `+socialPostCols, id, status))
}

func (s *Server) deleteSocialPost(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM social_posts WHERE id=$1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errSocialPostNotFound
	}
	return nil
}

func (s *Server) listSocialPostNotes(ctx context.Context, postID string) ([]SocialPostNote, error) {
	rows, err := s.db.Query(ctx, `
		SELECT n.id, n.post_id::text, n.author_id, COALESCE(u.name,''), n.content, n.created_at
		FROM social_post_notes n
		LEFT JOIN users u ON u.id = n.author_id
		WHERE n.post_id = $1::uuid ORDER BY n.created_at`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialPostNote{}
	for rows.Next() {
		var n SocialPostNote
		if err := rows.Scan(&n.ID, &n.PostID, &n.AuthorID, &n.AuthorName, &n.Content, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type SocialPostStatusHistory struct {
	ID        int64     `json:"id"`
	PostID    string    `json:"postId"`
	ChangedBy *int64    `json:"changedById"`
	Author    string    `json:"changedBy"`
	OldStatus string    `json:"oldStatus"`
	NewStatus string    `json:"newStatus"`
	ChangedAt time.Time `json:"changedAt"`
}

func (s *Server) listSocialPostStatusHistory(ctx context.Context, postID string) ([]SocialPostStatusHistory, error) {
	rows, err := s.db.Query(ctx, `
		SELECT h.id, h.post_id::text, h.changed_by, COALESCE(u.name,''), h.old_status, h.new_status, h.changed_at
		FROM social_post_status_history h
		LEFT JOIN users u ON u.id = h.changed_by
		WHERE h.post_id = $1::uuid ORDER BY h.changed_at DESC`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialPostStatusHistory{}
	for rows.Next() {
		var h SocialPostStatusHistory
		if err := rows.Scan(&h.ID, &h.PostID, &h.ChangedBy, &h.Author, &h.OldStatus, &h.NewStatus, &h.ChangedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Server) insertSocialPostStatusHistory(ctx context.Context, postID string, changedBy int64, oldStatus, newStatus string) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO social_post_status_history (post_id, changed_by, old_status, new_status)
		VALUES ($1::uuid, $2, $3, $4)`, postID, changedBy, oldStatus, newStatus)
	return err
}

func (s *Server) insertSocialPostNote(ctx context.Context, postID string, authorID int64, content string) (*SocialPostNote, error) {
	var n SocialPostNote
	err := s.db.QueryRow(ctx, `
		INSERT INTO social_post_notes (post_id, author_id, content)
		VALUES ($1::uuid, $2, $3)
		RETURNING id, post_id::text, author_id,
		          (SELECT COALESCE(name,'') FROM users WHERE id=author_id),
		          content, created_at`,
		postID, authorID, content).
		Scan(&n.ID, &n.PostID, &n.AuthorID, &n.AuthorName, &n.Content, &n.CreatedAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &n, nil
}

func (s *Server) listSocialPostPublishConfirmations(ctx context.Context, postID string) ([]SocialPostPublishConfirmation, error) {
	rows, err := s.db.Query(ctx, `
		SELECT c.platform, c.confirmed_by, COALESCE(u.name,''), c.confirmed_at
		FROM social_post_platform_confirmations c
		LEFT JOIN users u ON u.id = c.confirmed_by
		WHERE c.post_id = $1::uuid ORDER BY c.confirmed_at`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialPostPublishConfirmation{}
	for rows.Next() {
		var c SocialPostPublishConfirmation
		if err := rows.Scan(&c.Platform, &c.ConfirmedByID, &c.ConfirmedBy, &c.ConfirmedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// upsertSocialPostPublishConfirmation grava/atualiza a confirmação. confirmedBy
// é sempre o usuário autenticado (chamador nunca aceita esse valor do cliente).
func (s *Server) upsertSocialPostPublishConfirmation(ctx context.Context, postID, platform string, confirmedBy int64) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO social_post_platform_confirmations (post_id, platform, confirmed_by, confirmed_at)
		VALUES ($1::uuid, $2, $3, now())
		ON CONFLICT (post_id, platform) DO UPDATE SET confirmed_by = $3, confirmed_at = now()`,
		postID, platform, confirmedBy)
	return err
}

func (s *Server) deleteSocialPostPublishConfirmation(ctx context.Context, postID, platform string) error {
	_, err := s.db.Exec(ctx, `
		DELETE FROM social_post_platform_confirmations WHERE post_id=$1::uuid AND platform=$2`,
		postID, platform)
	return err
}

func (s *Server) listSocialPlatformOwners(ctx context.Context) ([]SocialPlatformOwner, error) {
	rows, err := s.db.Query(ctx, `
		SELECT o.platform, o.user_id, COALESCE(u.name,''), o.updated_at
		FROM social_platform_owners o
		JOIN users u ON u.id = o.user_id
		ORDER BY o.platform`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialPlatformOwner{}
	for rows.Next() {
		var o SocialPlatformOwner
		if err := rows.Scan(&o.Platform, &o.UserID, &o.UserName, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// getSocialPlatformOwner retorna nil (sem erro) se a plataforma não tiver dono configurado —
// esse é o caminho de fail-open, não uma condição de erro.
func (s *Server) getSocialPlatformOwner(ctx context.Context, platform string) (*SocialPlatformOwner, error) {
	var o SocialPlatformOwner
	err := s.db.QueryRow(ctx, `
		SELECT o.platform, o.user_id, COALESCE(u.name,''), o.updated_at
		FROM social_platform_owners o
		JOIN users u ON u.id = o.user_id
		WHERE o.platform = $1`, platform).
		Scan(&o.Platform, &o.UserID, &o.UserName, &o.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}

// setSocialPlatformOwner grava/atualiza o dono (upsert). updatedBy é sempre o usuário
// autenticado (chamador nunca aceita esse valor do cliente) — mesma convenção da confirmação.
func (s *Server) setSocialPlatformOwner(ctx context.Context, platform string, userID, updatedBy int64) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO social_platform_owners (platform, user_id, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (platform) DO UPDATE SET user_id = $2, updated_by = $3, updated_at = now()`,
		platform, userID, updatedBy)
	return err
}

func (s *Server) deleteSocialPlatformOwner(ctx context.Context, platform string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM social_platform_owners WHERE platform = $1`, platform)
	return err
}

// SocialSettings é a configuração fixa (não por post) do publicador universal —
// hoje só localização automática (ver social_publish.go). Linha única na tabela
// social_settings (seed garantida pela migração), por isso não tem ID de verdade.
type SocialSettings struct {
	InstagramLocationID string    `json:"instagramLocationId"`
	FacebookPlaceID     string    `json:"facebookPlaceId"`
	UpdatedByID         *int64    `json:"updatedById"`
	UpdatedByName       string    `json:"updatedByName"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

// getSocialSettings sempre acha a linha (a migração garante o seed) — erro aqui
// é de verdade erro de banco, não "sem configuração ainda".
func (s *Server) getSocialSettings(ctx context.Context) (*SocialSettings, error) {
	var st SocialSettings
	err := s.db.QueryRow(ctx, `
		SELECT st.instagram_location_id, st.facebook_place_id, st.updated_by,
		       COALESCE((SELECT name FROM users WHERE id = st.updated_by), ''), st.updated_at
		FROM social_settings st WHERE st.id = true`).
		Scan(&st.InstagramLocationID, &st.FacebookPlaceID, &st.UpdatedByID, &st.UpdatedByName, &st.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Server) updateSocialSettings(ctx context.Context, instagramLocationID, facebookPlaceID string, updatedBy int64) (*SocialSettings, error) {
	if _, err := s.db.Exec(ctx, `
		UPDATE social_settings SET instagram_location_id = $1, facebook_place_id = $2, updated_by = $3, updated_at = now()
		WHERE id = true`,
		instagramLocationID, facebookPlaceID, updatedBy); err != nil {
		return nil, err
	}
	return s.getSocialSettings(ctx)
}

// checkPublishConfirmationsComplete impõe a trava: só permite a transição pra
// "publicado" se toda plataforma de plataformasDestino já tiver confirmação
// registrada. Sem plataformas de destino definidas, não há o que checar.
func (s *Server) checkPublishConfirmationsComplete(ctx context.Context, postID string, plataformasDestino []string) error {
	if len(plataformasDestino) == 0 {
		return nil
	}
	confirmed, err := s.listSocialPostPublishConfirmations(ctx, postID)
	if err != nil {
		return err
	}
	confirmedSet := make(map[string]bool, len(confirmed))
	for _, c := range confirmed {
		confirmedSet[c.Platform] = true
	}
	for _, p := range plataformasDestino {
		if !confirmedSet[p] {
			return errPublishNotConfirmed
		}
	}
	return nil
}

var errPublishNotConfirmed = appErr(http.StatusBadRequest, "PUBLISH_NOT_CONFIRMED",
	"Não é possível concluir — publique o conteúdo em todas as redes definidas e confirme cada uma antes de marcar como concluído.")

var errSocialSerieNotFound = appErr(http.StatusNotFound, "SOCIAL_SERIE_NOT_FOUND", "Série não encontrada")

func (s *Server) listSocialSeries(ctx context.Context) ([]SocialSerie, error) {
	rows, err := s.db.Query(ctx, `SELECT id, nome, ativa, created_at FROM social_series ORDER BY nome`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialSerie{}
	for rows.Next() {
		var se SocialSerie
		if err := rows.Scan(&se.ID, &se.Nome, &se.Ativa, &se.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

func (s *Server) getSocialSerie(ctx context.Context, id int64) (*SocialSerie, error) {
	var se SocialSerie
	err := s.db.QueryRow(ctx, `SELECT id, nome, ativa, created_at FROM social_series WHERE id = $1`, id).
		Scan(&se.ID, &se.Nome, &se.Ativa, &se.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &se, nil
}

// validateSerieID só confirma que a série existe (FK garantiria isso no
// INSERT/UPDATE de qualquer forma, mas validar antes devolve um erro 400 com
// mensagem clara em vez de um 500 de violação de FK). Não exige Ativa=true:
// desativar uma série não pode quebrar a edição de posts que já a usam — só
// tira ela do dropdown de novos posts (ver GET /social/series no front).
func (s *Server) validateSerieID(ctx context.Context, id *int64) error {
	if id == nil {
		return nil
	}
	se, err := s.getSocialSerie(ctx, *id)
	if err != nil {
		return err
	}
	if se == nil {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Série inválida")
	}
	return nil
}

func (s *Server) insertSocialSerie(ctx context.Context, nome string) (*SocialSerie, error) {
	var se SocialSerie
	err := s.db.QueryRow(ctx,
		`INSERT INTO social_series (nome) VALUES ($1) RETURNING id, nome, ativa, created_at`, nome).
		Scan(&se.ID, &se.Nome, &se.Ativa, &se.CreatedAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &se, nil
}

func (s *Server) updateSocialSerie(ctx context.Context, id int64, nome string, ativa bool) (*SocialSerie, error) {
	var se SocialSerie
	err := s.db.QueryRow(ctx,
		`UPDATE social_series SET nome = $2, ativa = $3 WHERE id = $1 RETURNING id, nome, ativa, created_at`,
		id, nome, ativa).
		Scan(&se.ID, &se.Nome, &se.Ativa, &se.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &se, nil
}

var errSocialContaNotFound = appErr(http.StatusNotFound, "SOCIAL_CONTA_NOT_FOUND", "Conta não encontrada")

func (s *Server) listSocialContas(ctx context.Context) ([]SocialConta, error) {
	rows, err := s.db.Query(ctx, `SELECT id, nome, ativa, created_at FROM social_contas ORDER BY nome`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SocialConta{}
	for rows.Next() {
		var c SocialConta
		if err := rows.Scan(&c.ID, &c.Nome, &c.Ativa, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Server) getSocialConta(ctx context.Context, id int64) (*SocialConta, error) {
	var c SocialConta
	err := s.db.QueryRow(ctx, `SELECT id, nome, ativa, created_at FROM social_contas WHERE id = $1`, id).
		Scan(&c.ID, &c.Nome, &c.Ativa, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// validateContaID só confirma que a conta existe (FK garantiria isso no
// INSERT/UPDATE de qualquer forma, mas validar antes devolve um erro 400 com
// mensagem clara em vez de um 500 de violação de FK). Não exige Ativa=true,
// mesma lógica de validateSerieID.
func (s *Server) validateContaID(ctx context.Context, id *int64) error {
	if id == nil {
		return nil
	}
	c, err := s.getSocialConta(ctx, *id)
	if err != nil {
		return err
	}
	if c == nil {
		return appErr(http.StatusBadRequest, "BAD_REQUEST", "Conta inválida")
	}
	return nil
}

// resolveDefaultContaID busca o id da conta padrão (Santos Tech) — usada
// quando o post não informa contaId explicitamente (ver handleCreateSocialPost/
// handleUpdateSocialPost). A conta é seedada na migração; erro aqui é erro de
// banco de verdade, não "conta padrão ausente".
func (s *Server) resolveDefaultContaID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `SELECT id FROM social_contas WHERE nome = $1`, defaultSocialContaNome).Scan(&id)
	return id, err
}

func (s *Server) insertSocialConta(ctx context.Context, nome string) (*SocialConta, error) {
	var c SocialConta
	err := s.db.QueryRow(ctx,
		`INSERT INTO social_contas (nome) VALUES ($1) RETURNING id, nome, ativa, created_at`, nome).
		Scan(&c.ID, &c.Nome, &c.Ativa, &c.CreatedAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &c, nil
}

func (s *Server) updateSocialConta(ctx context.Context, id int64, nome string, ativa bool) (*SocialConta, error) {
	var c SocialConta
	err := s.db.QueryRow(ctx,
		`UPDATE social_contas SET nome = $2, ativa = $3 WHERE id = $1 RETURNING id, nome, ativa, created_at`,
		id, nome, ativa).
		Scan(&c.ID, &c.Nome, &c.Ativa, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, portalDBErr(err)
	}
	return &c, nil
}
