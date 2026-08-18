package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// maxSocialPublishMediaSize limita quanto bufferizamos em memória ao levar um
// arquivo do Drive (privado) pro R2 (público) antes de publicar — cobre foto
// e vídeo curto sem arriscar OOM num vídeo enorme colado por engano.
const maxSocialPublishMediaSize = 100 << 20 // 100MB

// socialPublisher publica a mídia de UM post em UMA plataforma e devolve o ID
// da publicação lá (só para auditoria — vira uma nota no post). mediaURL já
// chega pública e pronta (resolvida por resolveSocialPostMediaURL).
type socialPublisher interface {
	publishMedia(ctx context.Context, mediaURL, caption string, isVideo bool) (externalID string, err error)
}

// SocialPublishResult descreve o que aconteceu ao tentar publicar em UMA das
// plataformas de destino do post.
type SocialPublishResult struct {
	Platform   string `json:"platform"`
	Status     string `json:"status"` // "published" | "manual" | "failed" | "unsupported"
	ExternalID string `json:"externalId,omitempty"`
	Error      string `json:"error,omitempty"`
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
// diferentes pra cada tipo. `carrossel` (múltiplos itens) e `story` (endpoint
// próprio, regras de duração diferentes) ficam de fora do MVP: são
// reportados como "unsupported" em vez de arriscar publicar errado.
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

// PublishSocialPost dispara a publicação automática do post nas plataformas
// resolvidas por resolvePublishTargets que já têm adaptador plugado.
// Publicação com sucesso já confirma a plataforma no checklist (mesma tabela
// usada pela confirmação manual) e registra uma nota de auditoria com o ID
// externo — plataformas sem adaptador continuam exigindo confirmação manual,
// como hoje. `requestedPlatforms` vazio publica em toda plataformasDestino.
func (s *Server) PublishSocialPost(ctx context.Context, postID string, actingUserID int64, requestedPlatforms []string) ([]SocialPublishResult, error) {
	post, err := s.getSocialPost(ctx, postID)
	if err != nil {
		return nil, err
	}
	if post == nil {
		return nil, errSocialPostNotFound
	}
	if len(post.PlataformasDestino) == 0 {
		return nil, appErr(http.StatusBadRequest, "NO_TARGET_PLATFORMS", "Post sem plataformas de destino definidas")
	}
	targets, err := resolvePublishTargets(post.PlataformasDestino, requestedPlatforms)
	if err != nil {
		return nil, err
	}

	adapters := s.socialPublishAdapters()
	hasAdapter := false
	for _, p := range targets {
		if adapters[p] != nil {
			hasAdapter = true
			break
		}
	}
	if !hasAdapter {
		return nil, appErr(http.StatusBadRequest, "NO_AUTOMATED_PLATFORMS",
			"Nenhuma das plataformas de destino tem publicação automática configurada ainda — confirme manualmente no checklist.")
	}

	isVideo, supported := socialPostIsVideo(post.Formato)

	mediaURL, cleanup, err := s.resolveSocialPostMediaURL(ctx, post)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	results := make([]SocialPublishResult, 0, len(targets))
	for _, platform := range targets {
		adapter, ok := adapters[platform]
		if !ok {
			results = append(results, SocialPublishResult{Platform: platform, Status: "manual"})
			continue
		}
		if !supported {
			results = append(results, SocialPublishResult{Platform: platform, Status: "unsupported",
				Error: fmt.Sprintf("formato %q ainda não tem publicação automática (carrossel/story) — confirme manualmente", post.Formato)})
			continue
		}
		externalID, err := adapter.publishMedia(ctx, mediaURL, post.Caption, isVideo)
		if err != nil {
			slog.Error("social publish: falha ao publicar", "post_id", postID, "platform", platform, "err", err)
			results = append(results, SocialPublishResult{Platform: platform, Status: "failed", Error: err.Error()})
			continue
		}
		if err := s.upsertSocialPostPublishConfirmation(ctx, postID, platform, actingUserID); err != nil {
			slog.Error("social publish: publicou mas falhou ao confirmar checklist", "post_id", postID, "platform", platform, "err", err)
		}
		if _, err := s.insertSocialPostNote(ctx, postID, actingUserID,
			fmt.Sprintf("Publicado automaticamente em %s — ID %s", platform, externalID)); err != nil {
			slog.Warn("social publish: falha ao registrar nota de auditoria", "post_id", postID, "platform", platform, "err", err)
		}
		results = append(results, SocialPublishResult{Platform: platform, Status: "published", ExternalID: externalID})
	}
	return results, nil
}

// resolveSocialPostMediaURL devolve uma URL PÚBLICA para a mídia do post — as
// duas redes buscam o arquivo sozinhas, não aceitam upload binário nesses
// endpoints. Se o post referencia um arquivo do Drive (drive_file_id), baixa
// via service account (privado) e sobe pro R2 (público) sob uma chave
// temporária; a função de cleanup devolvida remove esse objeto do R2 depois
// que as chamadas de publicação já buscaram o arquivo. Sem arquivo do Drive,
// cai para media_url (URL já pública colada manualmente, comportamento
// anterior a esta feature).
func (s *Server) resolveSocialPostMediaURL(ctx context.Context, post *SocialPost) (mediaURL string, cleanup func(), err error) {
	noop := func() {}
	if post.DriveFileID == "" {
		if post.MediaURL == "" {
			return "", noop, appErr(http.StatusBadRequest, "NO_MEDIA", "Post sem mídia definida (nem arquivo do Drive, nem URL)")
		}
		return post.MediaURL, noop, nil
	}
	if s.drive == nil {
		return "", noop, appErr(http.StatusServiceUnavailable, "DRIVE_DISABLED", "Arquivos (Google Drive) não configurado")
	}
	if s.r2 == nil {
		return "", noop, appErr(http.StatusServiceUnavailable, "R2_DISABLED", "Upload público (R2) não configurado")
	}

	body, filename, contentType, _, _, err := s.drive.StreamDownload(ctx, post.DriveFileID, "")
	if err != nil {
		return "", noop, appErr(http.StatusBadGateway, "DRIVE_DOWNLOAD_FAILED", "Falha ao baixar o arquivo do Drive")
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxSocialPublishMediaSize+1))
	if err != nil {
		return "", noop, appErr(http.StatusBadGateway, "DRIVE_DOWNLOAD_FAILED", "Falha ao ler o arquivo do Drive")
	}
	if len(data) > maxSocialPublishMediaSize {
		return "", noop, appErr(http.StatusRequestEntityTooLarge, "MEDIA_TOO_LARGE", "Arquivo grande demais para publicar (máx 100MB)")
	}

	key := "social-publish/" + post.ID + "/" + sanitizeFilenameForHeader(filename)
	publicURL, err := s.r2.Upload(ctx, key, contentType, data)
	if err != nil {
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
