package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/oauth2/google"
)

const driveScope = "https://www.googleapis.com/auth/drive"

// DriveClient é um cliente mínimo da API do Google Drive v3 via service
// account — sem o SDK oficial (google.golang.org/api/drive/v3, que traz
// dezenas de dependências transitivas só pra 3 chamadas REST). Usa apenas
// golang.org/x/oauth2/google (já uma dependência do repo, ver server.go/login
// "Entrar com Google") pra autenticar e chama a API REST direto — mesmo
// espírito minimalista de r2.go: client HTTP próprio, nil = feature desligada.
//
// A service account é a ÚNICA identidade que o Drive conhece: o admin
// compartilha manualmente cada pasta com o e-mail dela no próprio Google
// Drive. Quem vê o quê dentro do dashboard é decidido só pela nossa ACL
// (drive_data.go/drive_access.go) — por isso downloads sempre passam pelo
// backend (StreamDownload), nunca um link direto do Drive pro navegador.
type DriveClient struct {
	http *http.Client // *http.Client do oauth2/jwt, renova o Bearer token sozinho
}

// newDriveClient devolve um cliente Drive, ou nil se a config estiver
// incompleta/inválida (feature off — rotas que dependem dele respondem 503).
func newDriveClient(cfg Config) *DriveClient {
	if cfg.GoogleDriveSAJSONB64 == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(cfg.GoogleDriveSAJSONB64)
	if err != nil {
		slog.Error("GOOGLE_DRIVE_SA_JSON_B64 inválido (base64) — Arquivos/Drive desabilitado", "err", err)
		return nil
	}
	jwtCfg, err := google.JWTConfigFromJSON(raw, driveScope)
	if err != nil {
		slog.Error("GOOGLE_DRIVE_SA_JSON_B64 inválido (JSON da service account) — Arquivos/Drive desabilitado", "err", err)
		return nil
	}
	return &DriveClient{http: jwtCfg.Client(context.Background())}
}

// DriveFile é o que expomos pro frontend — só o essencial da resposta do Drive.
type DriveFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	Size         int64  `json:"size"`
	ModifiedTime string `json:"modifiedTime"`
	IconLink     string `json:"iconLink,omitempty"`
}

type driveFileWire struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	Size         string `json:"size"` // Drive devolve como string; pastas não têm size
	ModifiedTime string `json:"modifiedTime"`
	IconLink     string `json:"iconLink"`
}

func (w driveFileWire) toDriveFile() DriveFile {
	size, _ := strconv.ParseInt(w.Size, 10, 64)
	return DriveFile{ID: w.ID, Name: w.Name, MimeType: w.MimeType, Size: size, ModifiedTime: w.ModifiedTime, IconLink: w.IconLink}
}

func driveAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("drive api %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
}

// ListFiles lista os arquivos (não-pasta, não-lixeira) dentro de driveFolderID.
func (d *DriveClient) ListFiles(ctx context.Context, driveFolderID string) ([]DriveFile, error) {
	escaped := strings.ReplaceAll(strings.ReplaceAll(driveFolderID, `\`, `\\`), `'`, `\'`)
	q := url.Values{}
	q.Set("q", fmt.Sprintf("'%s' in parents and trashed = false and mimeType != 'application/vnd.google-apps.folder'", escaped))
	q.Set("fields", "files(id,name,mimeType,size,modifiedTime,iconLink)")
	q.Set("pageSize", "200")
	q.Set("supportsAllDrives", "true")
	q.Set("includeItemsFromAllDrives", "true")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/drive/v3/files?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, driveAPIError(resp)
	}
	var out struct {
		Files []driveFileWire `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	files := make([]DriveFile, 0, len(out.Files))
	for _, f := range out.Files {
		files = append(files, f.toDriveFile())
	}
	return files, nil
}

// StreamDownload devolve o corpo do arquivo (chamador DEVE fechar) + nome +
// content-type. fileID não é validado contra driveFolderID aqui — o chamador
// (handler HTTP) já garantiu, via folderAccessGuard, que o usuário tem acesso
// à pasta antes de pedir o download de um arquivo listado dessa pasta.
func (d *DriveClient) StreamDownload(ctx context.Context, fileID string) (body io.ReadCloser, filename, contentType string, err error) {
	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?fields=name,mimeType&supportsAllDrives=true", nil)
	if err != nil {
		return nil, "", "", err
	}
	metaResp, err := d.http.Do(metaReq)
	if err != nil {
		return nil, "", "", err
	}
	defer metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK {
		return nil, "", "", driveAPIError(metaResp)
	}
	var meta struct {
		Name     string `json:"name"`
		MimeType string `json:"mimeType"`
	}
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		return nil, "", "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?alt=media&supportsAllDrives=true", nil)
	if err != nil {
		return nil, "", "", err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, "", "", driveAPIError(resp)
	}
	return resp.Body, meta.Name, meta.MimeType, nil
}

// UploadFile envia um arquivo pra dentro de driveFolderID via multipart
// (metadata JSON + conteúdo). O corpo é streamado direto de r pro Google via
// io.Pipe — não bufferiza o arquivo inteiro em memória (o chamador já limita
// o tamanho antes de chegar aqui, ver handleUploadDriveFile).
func (d *DriveClient) UploadFile(ctx context.Context, driveFolderID, filename, contentType string, r io.Reader) (DriveFile, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	boundary := mw.Boundary()

	go func() {
		metaPart, err := mw.CreatePart(map[string][]string{"Content-Type": {"application/json; charset=UTF-8"}})
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		meta, _ := json.Marshal(map[string]any{"name": filename, "parents": []string{driveFolderID}})
		if _, err := metaPart.Write(meta); err != nil {
			pw.CloseWithError(err)
			return
		}
		filePart, err := mw.CreatePart(map[string][]string{"Content-Type": {contentType}})
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(filePart, r); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := mw.Close(); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&supportsAllDrives=true&fields=id,name,mimeType,size,modifiedTime,iconLink",
		pr)
	if err != nil {
		return DriveFile{}, err
	}
	req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)

	resp, err := d.http.Do(req)
	if err != nil {
		return DriveFile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DriveFile{}, driveAPIError(resp)
	}
	var f driveFileWire
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return DriveFile{}, err
	}
	return f.toDriveFile(), nil
}
