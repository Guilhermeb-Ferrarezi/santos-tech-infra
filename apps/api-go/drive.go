package main

import (
	"bytes"
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

// driveFolderMimeType é o mimeType que o Drive usa pra pasta — usado tanto
// pra INCLUIR pastas na listagem (ListFiles devolve pasta e arquivo juntos;
// o frontend distingue pelo mimeType) quanto pra excluir da query de subida.
const driveFolderMimeType = "application/vnd.google-apps.folder"

// maxDriveAncestryDepth limita quantos níveis IsDescendant sobe checando
// `parents` antes de desistir — navegação legítima não passa disso, e um
// ID forjado pra escapar da pasta autorizada não consegue "provar" ancestry
// gastando um número ilimitado de chamadas à API.
const maxDriveAncestryDepth = 12

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

// driveListFilesMaxPages limita quantas páginas de 200 itens ListFiles segue
// (10 * 200 = 2000 itens) — teto generoso pra pastas normais, só existe pra
// não deixar uma pasta anormalmente grande (ou um bug de paginação do lado do
// Drive) fazer a listagem rodar indefinidamente.
const driveListFilesMaxPages = 10

// ListFiles lista o conteúdo (arquivos E subpastas, não-lixeira) dentro de
// driveFolderID — o chamador distingue pasta de arquivo pelo mimeType
// (driveFolderMimeType) pra navegação estilo Drive (entrar em subpasta).
// Segue `nextPageToken` internamente até esgotar ou até driveListFilesMaxPages
// — sem isso, uma pasta com mais de 200 itens perdia o resto silenciosamente
// (o chamador só via os 200 primeiros, sem nenhum sinal de que havia mais).
func (d *DriveClient) ListFiles(ctx context.Context, driveFolderID string) ([]DriveFile, error) {
	escaped := strings.ReplaceAll(strings.ReplaceAll(driveFolderID, `\`, `\\`), `'`, `\'`)
	files := make([]DriveFile, 0, 64)
	pageToken := ""
	for page := 0; page < driveListFilesMaxPages; page++ {
		q := url.Values{}
		q.Set("q", fmt.Sprintf("'%s' in parents and trashed = false", escaped))
		q.Set("fields", "nextPageToken,files(id,name,mimeType,size,modifiedTime,iconLink)")
		q.Set("orderBy", "folder,name")
		q.Set("pageSize", "200")
		q.Set("supportsAllDrives", "true")
		q.Set("includeItemsFromAllDrives", "true")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/drive/v3/files?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := d.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			err := driveAPIError(resp)
			resp.Body.Close()
			return nil, err
		}
		var out struct {
			Files         []driveFileWire `json:"files"`
			NextPageToken string          `json:"nextPageToken"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, f := range out.Files {
			files = append(files, f.toDriveFile())
		}
		if out.NextPageToken == "" {
			return files, nil
		}
		pageToken = out.NextPageToken
	}
	slog.Warn("ListFiles: atingiu o teto de páginas, lista pode estar incompleta",
		"folder", driveFolderID, "pages", driveListFilesMaxPages, "items", len(files))
	return files, nil
}

// DriveFolderInfo é uma pasta do Drive visível pela service account — usado
// pro admin ESCOLHER a pasta em vez de colar o ID/URL na mão.
type DriveFolderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListSharedFolders lista as pastas que a service account enxerga — ou seja,
// que já foram compartilhadas com ela no Drive (uma service account não tem
// "Meu Drive" próprio, então tudo que files.list devolve é compartilhamento
// explícito). Usado pro admin escolher a pasta num seletor em vez de colar
// o ID/URL manualmente.
func (d *DriveClient) ListSharedFolders(ctx context.Context) ([]DriveFolderInfo, error) {
	q := url.Values{}
	q.Set("q", "trashed = false and mimeType = 'application/vnd.google-apps.folder'")
	q.Set("fields", "files(id,name)")
	q.Set("pageSize", "200")
	q.Set("orderBy", "name")
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
		Files []DriveFolderInfo `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Files == nil {
		out.Files = []DriveFolderInfo{}
	}
	return out.Files, nil
}

// GetThumbnail busca a miniatura de um arquivo (`thumbnailLink`, gerado pelo
// Drive pra imagem/vídeo/PDF/documentos — não existe pra todo tipo) e devolve
// os bytes já baixados via o MESMO client autenticado da service account —
// thumbnailLink não é uma URL pública, precisa do Bearer token pra responder
// (por isso não dá pra apontar um <img src> direto pra ela do navegador).
// ok=false (sem erro) quando o arquivo não tem miniatura.
func (d *DriveClient) GetThumbnail(ctx context.Context, fileID string) (data []byte, contentType string, ok bool, err error) {
	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?fields=thumbnailLink&supportsAllDrives=true", nil)
	if err != nil {
		return nil, "", false, err
	}
	metaResp, err := d.http.Do(metaReq)
	if err != nil {
		return nil, "", false, err
	}
	defer metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK {
		return nil, "", false, driveAPIError(metaResp)
	}
	var meta struct {
		ThumbnailLink string `json:"thumbnailLink"`
	}
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		return nil, "", false, err
	}
	if meta.ThumbnailLink == "" {
		return nil, "", false, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.ThumbnailLink, nil)
	if err != nil {
		return nil, "", false, err
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, nil // thumbnailLink pode expirar/falhar — trata como "sem miniatura", não erro
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5MB de teto (miniatura é sempre pequena)
	if err != nil {
		return nil, "", false, err
	}
	contentType = resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return body, contentType, true, nil
}

// IsDescendant reporta se folderID é a própria rootID ou está dentro dela em
// qualquer profundidade — sobe por `parents` (um arquivo/pasta do Drive pode
// ter múltiplos pais; segue todos) até achar rootID, esgotar parents ou
// estourar maxDriveAncestryDepth. Usado pra impedir que alguém com acesso à
// pasta A navegue pra um ID de pasta arbitrário fora da árvore de A (a ACL é
// por pasta raiz, não por ID do Drive — ver handleListDriveFolderFiles).
func (d *DriveClient) IsDescendant(ctx context.Context, folderID, rootID string) (bool, error) {
	if folderID == rootID {
		return true, nil
	}
	frontier := []string{folderID}
	seen := map[string]bool{folderID: true}
	for depth := 0; depth < maxDriveAncestryDepth && len(frontier) > 0; depth++ {
		next := []string{}
		for _, id := range frontier {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet,
				"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(id)+"?fields=parents&supportsAllDrives=true", nil)
			if err != nil {
				return false, err
			}
			resp, err := d.http.Do(req)
			if err != nil {
				return false, err
			}
			var meta struct {
				Parents []string `json:"parents"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&meta)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				continue // pasta sumiu/sem acesso: só não conta como ancestral, não aborta a busca inteira
			}
			if decodeErr != nil {
				return false, decodeErr
			}
			for _, p := range meta.Parents {
				if p == rootID {
					return true, nil
				}
				if !seen[p] {
					seen[p] = true
					next = append(next, p)
				}
			}
		}
		frontier = next
	}
	return false, nil
}

// RenameFile troca o nome de um arquivo/subpasta no Drive. fileID não é
// validado contra nenhuma pasta aqui — o chamador (handler HTTP) garante via
// ensureFileInFolder que o arquivo está dentro da árvore autorizada.
func (d *DriveClient) RenameFile(ctx context.Context, fileID, name string) (DriveFile, error) {
	body, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		return DriveFile{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?supportsAllDrives=true&fields=id,name,mimeType,size,modifiedTime,iconLink",
		bytes.NewReader(body))
	if err != nil {
		return DriveFile{}, err
	}
	req.Header.Set("Content-Type", "application/json")
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

// TrashFile move um arquivo/subpasta pra lixeira do Drive — NÃO é exclusão
// permanente (reversível de lá pelo dono real da pasta no Drive), mesmo
// comportamento do botão "Remover" da UI do próprio Google Drive. fileID não
// é validado contra nenhuma pasta aqui — mesma nota de RenameFile.
func (d *DriveClient) TrashFile(ctx context.Context, fileID string) error {
	body, err := json.Marshal(map[string]bool{"trashed": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?supportsAllDrives=true",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return driveAPIError(resp)
	}
	return nil
}

// StreamDownload devolve o corpo do arquivo (chamador DEVE fechar), nome,
// content-type, status HTTP (200 ou 206) e os headers de range que devem ser
// espelhados na resposta (Content-Range/Accept-Ranges/Content-Length, só
// quando presentes). rangeHeader é repassado direto pro Drive — dá pra
// dar seek num vídeo sem baixar o arquivo inteiro. fileID não é validado
// contra nenhuma pasta aqui — o chamador (handler HTTP) garante via
// ensureFileInFolder que o arquivo está dentro da árvore autorizada.
func (d *DriveClient) StreamDownload(ctx context.Context, fileID, rangeHeader string) (body io.ReadCloser, filename, contentType string, status int, rangeHeaders http.Header, err error) {
	metaReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?fields=name,mimeType&supportsAllDrives=true", nil)
	if err != nil {
		return nil, "", "", 0, nil, err
	}
	metaResp, err := d.http.Do(metaReq)
	if err != nil {
		return nil, "", "", 0, nil, err
	}
	defer metaResp.Body.Close()
	if metaResp.StatusCode != http.StatusOK {
		return nil, "", "", 0, nil, driveAPIError(metaResp)
	}
	var meta struct {
		Name     string `json:"name"`
		MimeType string `json:"mimeType"`
	}
	if err := json.NewDecoder(metaResp.Body).Decode(&meta); err != nil {
		return nil, "", "", 0, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?alt=media&supportsAllDrives=true", nil)
	if err != nil {
		return nil, "", "", 0, nil, err
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, "", "", 0, nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		defer resp.Body.Close()
		return nil, "", "", 0, nil, driveAPIError(resp)
	}
	headers := http.Header{}
	for _, h := range []string{"Content-Range", "Accept-Ranges", "Content-Length"} {
		if v := resp.Header.Get(h); v != "" {
			headers.Set(h, v)
		}
	}
	return resp.Body, meta.Name, meta.MimeType, resp.StatusCode, headers, nil
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
