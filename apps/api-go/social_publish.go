package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

// maxSocialPublishMediaSize limita quanto bufferizamos em memória ao levar um
// arquivo do Drive (privado) pro R2 (público) antes de publicar — cobre foto
// e vídeo curto sem arriscar OOM num vídeo enorme colado por engano.
const maxSocialPublishMediaSize = 100 << 20 // 100MB

// publishOptions carrega os extras opcionais de uma publicação — cresce sem
// precisar mudar a assinatura de publishMedia toda vez que a Meta libera um
// parâmetro novo. coverURL só é usado em vídeo (capa custom do Reel);
// altText só em imagem estática (texto alternativo de acessibilidade).
// locationID/placeID vêm de social_settings (config fixa, não por post — ver
// GET/PUT /social/settings), cada client decide sozinho se/quando aplica
// (locationID no Instagram; placeID só em foto do Facebook, não vídeo).
type publishOptions struct {
	coverURL   string
	altText    string
	locationID string
	placeID    string
}

// socialPublisher publica a mídia de UM post em UMA plataforma e devolve o ID
// da publicação lá (só para auditoria — vira uma nota no post). mediaURL já
// chega pública e pronta (resolvida por resolveSocialPostMediaURL).
type socialPublisher interface {
	publishMedia(ctx context.Context, mediaURL, caption string, isVideo bool, opts publishOptions) (externalID string, err error)
}

// socialPublishAdapters resolve quais plataformas têm publicação automática
// plugada AGORA. Uma plataforma ausente daqui não é erro — ela continua no
// checklist manual que já existia antes desta feature; é assim que o
// publicador cresce (Threads, YouTube, ...) sem arriscar o que já funciona.
func (s *Server) socialPublishAdapters() map[string]socialPublisher {
	adapters := map[string]socialPublisher{}
	if s.instagram != nil && s.instagram.enabled() {
		adapters["instagram"] = s.instagram
	}
	if s.facebook != nil && s.facebook.enabled() {
		adapters["facebook"] = s.facebook
	}
	return adapters
}

// videoFormatos/imageFormatos classificam o campo `formato` do post pra saber
// se a mídia é foto ou vídeo — as duas redes usam endpoints/parâmetros
// diferentes pra cada tipo. `story` (endpoint próprio, regras de duração
// diferentes) fica de fora do MVP: é reportado como "unsupported" em vez de
// arriscar publicar errado. `carrossel` NÃO passa por aqui — tem despacho
// próprio em publishCarouselPost (suporte real, só no Instagram).
var socialVideoFormatos = map[string]bool{"reel": true, "short": true, "video_longo": true}
var socialImageFormatos = map[string]bool{"estatico": true, "thumbnail": true, "card_link": true}

func socialPostIsVideo(formato string) (isVideo, supported bool) {
	if socialVideoFormatos[formato] {
		return true, true
	}
	if socialImageFormatos[formato] {
		return false, true
	}
	return false, false
}

// carouselItemRef é o shape salvo em social_posts.carousel_items (JSON) —
// itens 2..10 de um carrossel; o item 1 usa o trio
// drive_folder_id/drive_file_id/drive_file_name do próprio post.
type carouselItemRef struct {
	FolderID string `json:"folderId"`
	FileID   string `json:"fileId"`
	FileName string `json:"fileName"`
}

// carouselMediaItem é um item de carrossel já resolvido pra URL pública,
// pronto pra virar um container filho na Graph API do Instagram.
type carouselMediaItem struct {
	URL     string
	IsVideo bool
}

var carouselVideoExtensionRe = regexp.MustCompile(`(?i)\.(mp4|mov|webm|m4v|avi|mkv)$`)

// carouselItemIsVideo decide foto/vídeo pela EXTENSÃO do arquivo (mesma
// lógica do front em PublishReviewDialog.tsx) — um carrossel pode misturar
// imagem e vídeo, então não dá pra usar o `formato` do post inteiro como em
// socialPostIsVideo.
func carouselItemIsVideo(fileName string) bool {
	return carouselVideoExtensionRe.MatchString(fileName)
}

// parseCarouselItems monta a lista completa de itens do carrossel (item 1 do
// trio principal do post + carousel_items) e valida o teto de 10 da Graph
// API. Erro se tiver menos de 2 (carrossel de verdade precisa de pelo menos
// 2 itens) ou mais de 10.
func parseCarouselItems(post *SocialPost) ([]carouselItemRef, error) {
	items := []carouselItemRef{}
	if post.DriveFileID != "" {
		folderID := ""
		if post.DriveFolderID != nil {
			folderID = *post.DriveFolderID
		}
		items = append(items, carouselItemRef{FolderID: folderID, FileID: post.DriveFileID, FileName: post.DriveFileName})
	}
	if len(post.CarouselItems) > 0 {
		var extra []carouselItemRef
		if err := json.Unmarshal(post.CarouselItems, &extra); err != nil {
			return nil, appErr(http.StatusInternalServerError, "CAROUSEL_ITEMS_CORRUPTED", "Itens do carrossel corrompidos")
		}
		items = append(items, extra...)
	}
	if len(items) < 2 {
		return nil, appErr(http.StatusBadRequest, "CAROUSEL_NOT_ENOUGH_ITEMS",
			"Carrossel precisa de pelo menos 2 itens de mídia escolhidos")
	}
	if len(items) > 10 {
		return nil, appErr(http.StatusBadRequest, "CAROUSEL_TOO_MANY_ITEMS", "Carrossel aceita no máximo 10 itens")
	}
	return items, nil
}

// resolvePublishTargets decide QUAIS plataformas publicar nesta chamada:
// sem `requested` (vazio), publica em toda plataformasDestino (comportamento
// original); com `requested`, restringe a esse subconjunto — usado pelo
// diálogo de revisão pra deixar de fora uma plataforma já confirmada (evita
// publicar de novo e duplicar o post na rede, já que publicar não edita um
// post existente, sempre CRIA um novo). Pedir uma plataforma fora de
// plataformasDestino é erro — não é "publicar em qualquer coisa".
func resolvePublishTargets(destino, requested []string) ([]string, error) {
	if len(requested) == 0 {
		return destino, nil
	}
	destinoSet := make(map[string]bool, len(destino))
	for _, d := range destino {
		destinoSet[d] = true
	}
	for _, r := range requested {
		if !destinoSet[r] {
			return nil, appErr(http.StatusBadRequest, "PLATFORM_NOT_TARGET",
				fmt.Sprintf("%q não é uma plataforma de destino deste post", r))
		}
	}
	return requested, nil
}

// captionWithHashtags monta o texto que de fato vai pra rede: a legenda
// (bloco "Legenda + hashtags" do editorial) com as hashtags coladas no fim,
// separadas por uma linha em branco — hashtags moram em coluna própria
// (social_posts.hashtags) e nunca eram enviadas junto na publicação
// automática antes desta função existir.
func captionWithHashtags(caption string, hashtags []string) string {
	if len(hashtags) == 0 {
		return caption
	}
	tags := strings.Join(hashtags, " ")
	if caption == "" {
		return tags
	}
	return caption + "\n\n" + tags
}

// preparePublish faz a parte RÁPIDA e síncrona de uma publicação — uma
// leitura no banco + validações puras, sem chamar rede nenhuma (Drive/R2/Graph
// API). Erros aqui viram resposta HTTP imediata (400/404), como sempre foi.
// A parte LENTA (rede) é responsabilidade do caller rodar à parte — ver
// runPublish — porque só o Instagram processando um vídeo pode levar
// minutos, bem além de qualquer timeout de request HTTP razoável.
func (s *Server) preparePublish(ctx context.Context, postID string, requestedPlatforms []string) (post *SocialPost, targets []string, adapters map[string]socialPublisher, caption string, err error) {
	post, err = s.getSocialPost(ctx, postID)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if post == nil {
		return nil, nil, nil, "", errSocialPostNotFound
	}
	if len(post.PlataformasDestino) == 0 {
		return nil, nil, nil, "", appErr(http.StatusBadRequest, "NO_TARGET_PLATFORMS", "Post sem plataformas de destino definidas")
	}
	targets, err = resolvePublishTargets(post.PlataformasDestino, requestedPlatforms)
	if err != nil {
		return nil, nil, nil, "", err
	}

	adapters = s.socialPublishAdapters()
	hasAdapter := false
	for _, p := range targets {
		if adapters[p] != nil {
			hasAdapter = true
			break
		}
	}
	if !hasAdapter {
		return nil, nil, nil, "", appErr(http.StatusBadRequest, "NO_AUTOMATED_PLATFORMS",
			"Nenhuma das plataformas de destino tem publicação automática configurada ainda — confirme manualmente no checklist.")
	}

	caption = captionWithHashtags(post.Caption, post.Hashtags)
	return post, targets, adapters, caption, nil
}

// runPublish é a parte LENTA da publicação (Drive → R2 → Graph API) — chame
// numa goroutine própria, com um ctx DESCOLADO da requisição HTTP que a
// disparou (context.WithoutCancel no handler). Sem isso, o request HTTP
// morre no meio: o Instagram sozinho pode levar minutos processando o vídeo
// (waitMediaFinished), bem além do WriteTimeout do servidor, do timeout do
// proxy da Cloudflare ou do navegador — foi exatamente isso que quebrava
// antes ("context canceled" nos dois lados, Instagram e Facebook, mesmo a
// publicação real podendo ter dado certo segundos depois).
//
// Por rodar solta, fora da cadeia normal do http.Server, NÃO tem o recover de
// pânico do middleware por cima — por isso recupera pânico aqui mesmo, senão
// um pânico nessa goroutine derruba o processo inteiro.
//
// Resultado não volta por HTTP pra ninguém: sucesso confirma a plataforma no
// checklist (mesma tabela da confirmação manual) e grava uma nota de
// auditoria com o ID externo; falha grava uma nota de auditoria com o
// motivo, sem confirmar nada — é assim que o client (GET /social/posts/{id})
// sabe o que aconteceu depois de disparar.
func (s *Server) runPublish(ctx context.Context, post *SocialPost, targets []string, adapters map[string]socialPublisher, actingUserID int64, caption string) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("social publish: pânico recuperado", "post_id", post.ID, "panic", rec)
		}
	}()

	if post.Formato == "carrossel" {
		s.publishCarouselPost(ctx, post, targets, adapters, actingUserID, caption)
		return
	}

	isVideo, supported := socialPostIsVideo(post.Formato)
	mediaURL, cleanup, err := s.resolveSocialPostMediaURL(ctx, post)
	if err != nil {
		slog.Error("social publish: falha ao resolver mídia", "post_id", post.ID, "err", err)
		s.notePublishFailure(ctx, post.ID, actingUserID, targets, err)
		return
	}
	defer cleanup()

	settings, err := s.getSocialSettings(ctx)
	if err != nil {
		slog.Error("social publish: falha ao carregar configurações", "post_id", post.ID, "err", err)
		s.notePublishFailure(ctx, post.ID, actingUserID, targets, err)
		return
	}
	opts := publishOptions{altText: post.AltText, locationID: settings.InstagramLocationID, placeID: settings.FacebookPlaceID}
	if isVideo {
		coverURL, coverCleanup, err := s.resolveSocialPostCoverURL(ctx, post)
		if err != nil {
			slog.Error("social publish: falha ao resolver capa", "post_id", post.ID, "err", err)
			s.notePublishFailure(ctx, post.ID, actingUserID, targets, err)
			return
		}
		defer coverCleanup()
		opts.coverURL = coverURL
	}

	for _, platform := range targets {
		adapter, ok := adapters[platform]
		if !ok {
			continue // sem adaptador: segue no checklist manual, como sempre
		}
		if !supported {
			slog.Warn("social publish: formato sem publicação automática", "post_id", post.ID, "platform", platform, "formato", post.Formato)
			s.insertPublishFailureNote(ctx, post.ID, actingUserID, platform,
				fmt.Sprintf("formato %q ainda não tem publicação automática (story) — confirme manualmente", post.Formato))
			continue
		}
		externalID, err := adapter.publishMedia(ctx, mediaURL, caption, isVideo, opts)
		if err != nil {
			slog.Error("social publish: falha ao publicar", "post_id", post.ID, "platform", platform, "err", err)
			s.insertPublishFailureNote(ctx, post.ID, actingUserID, platform, err.Error())
			continue
		}
		if err := s.upsertSocialPostPublishConfirmation(ctx, post.ID, platform, actingUserID); err != nil {
			slog.Error("social publish: publicou mas falhou ao confirmar checklist", "post_id", post.ID, "platform", platform, "err", err)
		}
		if _, err := s.insertSocialPostNote(ctx, post.ID, actingUserID,
			fmt.Sprintf("Publicado automaticamente em %s — ID %s", platform, externalID)); err != nil {
			slog.Warn("social publish: falha ao registrar nota de auditoria", "post_id", post.ID, "platform", platform, "err", err)
		}
	}
}

// notePublishFailure registra uma falha que impede TODAS as plataformas desta
// chamada de sequer tentar publicar (ex.: mídia não resolveu) — sem isso, uma
// publicação assíncrona que falha cedo desaparece sem deixar rastro nenhum
// pro usuário.
func (s *Server) notePublishFailure(ctx context.Context, postID string, actingUserID int64, targets []string, err error) {
	msg := fmt.Sprintf("Falha ao preparar a publicação automática (%s): %s", strings.Join(targets, ", "), err.Error())
	if _, noteErr := s.insertSocialPostNote(ctx, postID, actingUserID, msg); noteErr != nil {
		slog.Warn("social publish: falha ao registrar nota de auditoria", "post_id", postID, "err", noteErr)
	}
}

// insertPublishFailureNote registra a falha de UMA plataforma específica.
func (s *Server) insertPublishFailureNote(ctx context.Context, postID string, actingUserID int64, platform, reason string) {
	msg := fmt.Sprintf("Falha ao publicar automaticamente em %s: %s", platform, reason)
	if _, err := s.insertSocialPostNote(ctx, postID, actingUserID, msg); err != nil {
		slog.Warn("social publish: falha ao registrar nota de auditoria", "post_id", postID, "platform", platform, "err", err)
	}
}

// publishCarouselPost despacha um post formato=carrossel. Suporte real (via
// children da Graph API) só existe pro Instagram por enquanto — publicar
// carrossel multi-foto na Página do Facebook é um fluxo de API bem diferente
// (unpublished photos + /feed com attached_media) que ainda não vale o
// esforço/risco dado o volume de uso atual; outras plataformas continuam no
// checklist manual, como qualquer plataforma sem adaptador.
func (s *Server) publishCarouselPost(ctx context.Context, post *SocialPost, targets []string, adapters map[string]socialPublisher, actingUserID int64, caption string) {
	items, err := parseCarouselItems(post)
	if err != nil {
		slog.Error("social publish: carrossel inválido", "post_id", post.ID, "err", err)
		s.notePublishFailure(ctx, post.ID, actingUserID, targets, err)
		return
	}

	var resolved []carouselMediaItem
	var resolvedCleanup func()
	for _, platform := range targets {
		if platform != "instagram" || adapters["instagram"] == nil {
			continue // sem suporte automático fora do Instagram: manual, como sempre
		}
		if resolved == nil {
			resolved, resolvedCleanup, err = s.resolveCarouselItemURLs(ctx, post.ID, items)
			if err != nil {
				slog.Error("social publish: falha ao resolver itens do carrossel", "post_id", post.ID, "err", err)
				s.notePublishFailure(ctx, post.ID, actingUserID, targets, err)
				return
			}
			defer resolvedCleanup()
		}
		externalID, err := s.instagram.publishCarousel(ctx, resolved, caption)
		if err != nil {
			slog.Error("social publish: falha ao publicar carrossel", "post_id", post.ID, "platform", platform, "err", err)
			s.insertPublishFailureNote(ctx, post.ID, actingUserID, platform, err.Error())
			continue
		}
		if err := s.upsertSocialPostPublishConfirmation(ctx, post.ID, platform, actingUserID); err != nil {
			slog.Error("social publish: publicou mas falhou ao confirmar checklist", "post_id", post.ID, "platform", platform, "err", err)
		}
		if _, err := s.insertSocialPostNote(ctx, post.ID, actingUserID,
			fmt.Sprintf("Carrossel publicado automaticamente em %s — ID %s", platform, externalID)); err != nil {
			slog.Warn("social publish: falha ao registrar nota de auditoria", "post_id", post.ID, "platform", platform, "err", err)
		}
	}
}

// resolveDriveFileToPublicURL baixa UM arquivo privado do Drive (service
// account) e sobe pro R2 (público) sob uma chave temporária — extraído de
// resolveSocialPostMediaURL pra ser reaproveitado pela mídia principal, pela
// capa do Reel e por cada item de um carrossel. A função de cleanup devolvida
// remove o objeto do R2 depois que a Graph API já buscou o arquivo.
func (s *Server) resolveDriveFileToPublicURL(ctx context.Context, keyPrefix, driveFileID string) (publicURL string, cleanup func(), err error) {
	noop := func() {}
	if s.drive == nil {
		return "", noop, appErr(http.StatusServiceUnavailable, "DRIVE_DISABLED", "Arquivos (Google Drive) não configurado")
	}
	if s.r2 == nil {
		return "", noop, appErr(http.StatusServiceUnavailable, "R2_DISABLED", "Upload público (R2) não configurado")
	}

	body, filename, contentType, _, _, err := s.drive.StreamDownload(ctx, driveFileID, "")
	if err != nil {
		slog.Error("social publish: falha ao baixar arquivo do Drive", "drive_file_id", driveFileID, "err", err)
		return "", noop, appErr(http.StatusBadGateway, "DRIVE_DOWNLOAD_FAILED", "Falha ao baixar o arquivo do Drive")
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxSocialPublishMediaSize+1))
	if err != nil {
		slog.Error("social publish: falha ao ler arquivo do Drive", "drive_file_id", driveFileID, "err", err)
		return "", noop, appErr(http.StatusBadGateway, "DRIVE_DOWNLOAD_FAILED", "Falha ao ler o arquivo do Drive")
	}
	if len(data) > maxSocialPublishMediaSize {
		return "", noop, appErr(http.StatusRequestEntityTooLarge, "MEDIA_TOO_LARGE", "Arquivo grande demais para publicar (máx 100MB)")
	}

	key := keyPrefix + "/" + sanitizeFilenameForHeader(filename)
	publicURL, err = s.r2.Upload(ctx, key, contentType, data)
	if err != nil {
		slog.Error("social publish: falha ao subir mídia pro R2", "key", key, "content_type", contentType, "bytes", len(data), "err", err)
		return "", noop, appErr(http.StatusBadGateway, "R2_UPLOAD_FAILED", "Falha ao preparar a mídia para publicação")
	}
	cleanup = func() {
		// best-effort: as plataformas já buscaram o arquivo a essa altura —
		// falha aqui só deixa lixo temporário no bucket, não é crítico.
		if err := s.r2.Delete(context.WithoutCancel(ctx), key); err != nil {
			slog.Warn("social publish: falha ao limpar mídia temporária do R2", "key", key, "err", err)
		}
	}
	return publicURL, cleanup, nil
}

// resolveSocialPostMediaURL devolve uma URL PÚBLICA para a mídia PRINCIPAL do
// post — as duas redes buscam o arquivo sozinhas, não aceitam upload binário
// nesses endpoints. Se o post referencia um arquivo do Drive (drive_file_id),
// baixa e sobe pro R2 via resolveDriveFileToPublicURL; sem arquivo do Drive,
// cai para media_url (URL já pública colada manualmente, comportamento
// anterior a essa feature de Drive).
func (s *Server) resolveSocialPostMediaURL(ctx context.Context, post *SocialPost) (mediaURL string, cleanup func(), err error) {
	if post.DriveFileID == "" {
		if post.MediaURL == "" {
			return "", func() {}, appErr(http.StatusBadRequest, "NO_MEDIA", "Post sem mídia definida (nem arquivo do Drive, nem URL)")
		}
		return post.MediaURL, func() {}, nil
	}
	return s.resolveDriveFileToPublicURL(ctx, "social-publish/"+post.ID, post.DriveFileID)
}

// resolveSocialPostCoverURL devolve a URL pública da capa customizada do
// Reel, se o post tiver uma escolhida — cover_url é opcional, sem ela a Meta
// usa o frame 0 do vídeo (que costuma sair borrado/no meio de um corte).
func (s *Server) resolveSocialPostCoverURL(ctx context.Context, post *SocialPost) (coverURL string, cleanup func(), err error) {
	if post.DriveCoverFileID == "" {
		return "", func() {}, nil
	}
	return s.resolveDriveFileToPublicURL(ctx, "social-publish/"+post.ID+"/cover", post.DriveCoverFileID)
}

// resolveCarouselItemURLs resolve TODOS os itens de um carrossel pra URL
// pública, uma chave de R2 por item (índice no nome evita colisão quando dois
// itens vêm do mesmo arquivo por engano). Cleanup agregado remove todo mundo
// de uma vez; se algum item falhar no meio, os já resolvidos são limpos antes
// de propagar o erro (não deixa lixo órfão no R2 numa publicação que não vai
// pra frente).
func (s *Server) resolveCarouselItemURLs(ctx context.Context, postID string, items []carouselItemRef) ([]carouselMediaItem, func(), error) {
	resolved := make([]carouselMediaItem, 0, len(items))
	cleanups := make([]func(), 0, len(items))
	cleanupAll := func() {
		for _, c := range cleanups {
			c()
		}
	}
	for i, item := range items {
		keyPrefix := fmt.Sprintf("social-publish/%s/carousel-%d", postID, i+1)
		url, cleanup, err := s.resolveDriveFileToPublicURL(ctx, keyPrefix, item.FileID)
		if err != nil {
			cleanupAll()
			return nil, func() {}, err
		}
		cleanups = append(cleanups, cleanup)
		resolved = append(resolved, carouselMediaItem{URL: url, IsVideo: carouselItemIsVideo(item.FileName)})
	}
	return resolved, cleanupAll, nil
}
