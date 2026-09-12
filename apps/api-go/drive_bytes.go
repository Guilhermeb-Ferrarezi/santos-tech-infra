package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// DownloadBytes é a irmã de StreamDownload pra quem precisa do CONTEÚDO em
// memória, não de um stream pro navegador: lê no máximo `limit` bytes e
// descarta o resto. Existe pro Pós-aula colar o código-fonte anexado ao
// diário no brief do Claude — por isso o teto é pequeno (64 KB) e o
// chamador decide o que fazer com um arquivo maior (aqui: truncar).
// Não autoriza nada — quem chama já decidiu que pode ler esse fileID.
func (d *DriveClient) DownloadBytes(ctx context.Context, fileID string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("drive: limite inválido %d", limit)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?alt=media&supportsAllDrives=true", nil)
	if err != nil {
		return nil, err
	}
	// d.http (com timeout de API, 30s): 64 KB cabem folgado; o stream sem
	// timeout é pra vídeo, não pra isto.
	resp, err := d.do(d.http, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, driveAPIError(resp)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
