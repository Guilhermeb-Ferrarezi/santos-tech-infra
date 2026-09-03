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
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
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
	// http: chamadas de API (metadados, listagem, rename, move...). Tem
	// Timeout no cliente porque são requisições curtas — sem ele, uma
	// requisição pendurada segurava um handler pra sempre (o R2 em r2.go já
	// fazia certo).
	http *http.Client
	// stream: transferência do conteúdo do arquivo (download e upload). NÃO
	// pode ter Timeout no cliente: http.Client.Timeout cobre também a leitura
	// do corpo, e cortaria no meio o download de um vídeo ou o upload de
	// 500MB numa conexão lenta. O limite aqui é o ctx da request (cancelado
	// quando o navegador desiste) mais os timeouts de dial/TLS do transport.
	stream *http.Client
	// uploadStream: mesma política de stream (sem Timeout), mas autenticado
	// como uma conta Google real via OAuth em vez da service account — só
	// UploadFile usa isto. Nil quando as credenciais OAuth não estão
	// configuradas, e UploadFile cai para stream (service account), que
	// resulta no já conhecido 403 storageQuotaExceeded — ver comentário em
	// Config.GoogleDriveOAuthClientID.
	uploadStream *http.Client
}

const (
	// driveAPITimeout: teto por requisição de API do Drive.
	driveAPITimeout = 30 * time.Second
	// driveMaxAttempts / driveRetryBaseDelay: retry com backoff exponencial
	// (300ms, 600ms) em 429/5xx e em erro de rede — o Drive devolve 429 com
	// alguma frequência e sem retry isso virava erro na cara do usuário.
	driveMaxAttempts    = 3
	driveRetryBaseDelay = 300 * time.Millisecond
)

// driveRetryableStatus: 429 (rate limit do Google) e 5xx são transitórios; 4xx
// (exceto 429) é definitivo e repetir só gasta quota.
func driveRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// do executa a requisição com retry em erro transitório. Só repete quando o
// corpo é replayável (sem corpo, ou com GetBody — o que http.NewRequest
// preenche sozinho para bytes.Reader): o upload streama de um io.Pipe, que não
// dá pra reenviar, e nesse caso vai uma tentativa só.
func (d *DriveClient) do(client *http.Client, req *http.Request) (*http.Response, error) {
	replayable := req.Body == nil || req.GetBody != nil
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if req.GetBody != nil {
				b, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				req.Body = b
			}
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(driveRetryBaseDelay << (attempt - 1)):
			}
		}
		resp, err := client.Do(req)
		if !replayable || attempt >= driveMaxAttempts-1 {
			return resp, err
		}
		if err != nil {
			continue // erro de rede: tenta de novo
		}
		if !driveRetryableStatus(resp.StatusCode) {
			return resp, nil
		}
		// Drena um pedaço do corpo pra devolver a conexão ao pool e repete.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
	}
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
	// jwtCfg.Client devolve um cliente SEM Timeout; reaproveitamos o Transport
	// dele (é quem injeta e renova o Bearer da service account) em dois
	// clientes com políticas de timeout diferentes.
	oauthTransport := jwtCfg.Client(context.Background()).Transport
	return &DriveClient{
		http:         &http.Client{Transport: oauthTransport, Timeout: driveAPITimeout},
		stream:       &http.Client{Transport: oauthTransport},
		uploadStream: newDriveUploadOAuthClient(cfg),
	}
}

// newDriveUploadOAuthClient monta o client autenticado como usuário real (ver
// comentário em Config.GoogleDriveOAuthClientID) — nil se as 3 credenciais
// não estiverem todas presentes, e UploadFile cai de volta pra service
// account nesse caso.
func newDriveUploadOAuthClient(cfg Config) *http.Client {
	if cfg.GoogleDriveOAuthClientID == "" || cfg.GoogleDriveOAuthClientSecret == "" || cfg.GoogleDriveOAuthRefreshToken == "" {
		return nil
	}
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.GoogleDriveOAuthClientID,
		ClientSecret: cfg.GoogleDriveOAuthClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{driveScope},
	}
	token := &oauth2.Token{RefreshToken: cfg.GoogleDriveOAuthRefreshToken}
	return oauthCfg.Client(context.Background(), token)
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
		resp, err := d.do(d.http, req)
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
	resp, err := d.do(d.http, req)
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
	metaResp, err := d.do(d.http, metaReq)
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
	resp, err := d.do(d.http, req)
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
//
// Um item que o Drive recusa (404/403: sumiu, foi pra lixeira, ou a service
// account não enxerga) simplesmente não conta como ancestral e a busca segue
// pelos outros pais. Já 429/5xx é falha transitória e vira ERRO: antes qualquer
// status != 200 era tratado como "não é ancestral", então um rate limit do
// Google respondia "acesso negado" — e o false ainda ia parar no cache de 5min,
// negando acesso legítimo muito depois do Drive já ter voltado.
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
			resp, err := d.do(d.http, req)
			if err != nil {
				return false, err
			}
			var meta struct {
				Parents []string `json:"parents"`
			}
			status := resp.StatusCode
			decodeErr := json.NewDecoder(resp.Body).Decode(&meta)
			resp.Body.Close()
			if status != http.StatusOK {
				if driveRetryableStatus(status) {
					// d.do já repetiu e não adiantou: falha transitória do
					// Drive não pode virar "não é ancestral" (= acesso negado).
					return false, fmt.Errorf("drive ancestry %s: status %d", id, status)
				}
				continue // sumiu/sem acesso: só não conta como ancestral, não aborta a busca inteira
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
	resp, err := d.do(d.http, req)
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
	resp, err := d.do(d.http, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return driveAPIError(resp)
	}
	return nil
}

// CreateFolder cria uma subpasta dentro de parentID — o chamador (handler
// HTTP) garante via ensureFileInFolder/IsDescendant que parentID está dentro
// da árvore autorizada antes de chegar aqui.
func (d *DriveClient) CreateFolder(ctx context.Context, parentID, name string) (DriveFile, error) {
	body, err := json.Marshal(map[string]any{
		"name":     name,
		"mimeType": driveFolderMimeType,
		"parents":  []string{parentID},
	})
	if err != nil {
		return DriveFile{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.googleapis.com/drive/v3/files?supportsAllDrives=true&fields=id,name,mimeType,size,modifiedTime,iconLink",
		bytes.NewReader(body))
	if err != nil {
		return DriveFile{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.do(d.http, req)
	if err != nil {
		return DriveFile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return DriveFile{}, driveAPIError(resp)
	}
	var f driveFileWire
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return DriveFile{}, err
	}
	return f.toDriveFile(), nil
}

// getParents devolve os parents ATUAIS de fileID — precisa disso antes de
// mover, já que o Drive exige listar explicitamente quem sai (removeParents)
// e quem entra (addParents), não tem um "parent" único mutável direto.
func (d *DriveClient) getParents(ctx context.Context, fileID string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?fields=parents&supportsAllDrives=true", nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.do(d.http, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, driveAPIError(resp)
	}
	var meta struct {
		Parents []string `json:"parents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
	}
	return meta.Parents, nil
}

// MoveFile troca o parent de um arquivo/subpasta (equivalente a "mover" no
// Drive, que não tem um único "parent" mutável — files.update com
// addParents/removeParents é o jeito oficial). Busca os parents atuais
// primeiro (pra saber quem remover). O chamador garante via ensureFileInFolder
// que fileID e toParentID estão dentro da árvore que o usuário tem acesso de
// escrita.
func (d *DriveClient) MoveFile(ctx context.Context, fileID, toParentID string) (DriveFile, error) {
	current, err := d.getParents(ctx, fileID)
	if err != nil {
		return DriveFile{}, err
	}

	q := url.Values{}
	q.Set("supportsAllDrives", "true")
	q.Set("addParents", toParentID)
	if len(current) > 0 {
		q.Set("removeParents", strings.Join(current, ","))
	}
	q.Set("fields", "id,name,mimeType,size,modifiedTime,iconLink")

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		"https://www.googleapis.com/drive/v3/files/"+url.PathEscape(fileID)+"?"+q.Encode(), nil)
	if err != nil {
		return DriveFile{}, err
	}
	resp, err := d.do(d.http, req)
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
	metaResp, err := d.do(d.http, metaReq)
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
	// d.stream (sem Timeout de cliente): o corpo é o arquivo inteiro e vai ser
	// lido pelo handler enquanto streama pro navegador.
	resp, err := d.do(d.stream, req)
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
		// recover: sem isto, um panic aqui dentro (ex.: r.Read explodindo no
		// meio do upload) derruba o processo inteiro — toda goroutine de fundo
		// do serviço segue essa convenção (ver safeGo em util.go), só esta
		// tinha ficado de fora.
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic no upload de arquivo pro Drive", "panic", rec, "stack", string(debug.Stack()))
				pw.CloseWithError(fmt.Errorf("panic no upload: %v", rec))
			}
		}()
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

	// Prefere uploadStream (usuário real, tem cota) — stream (service
	// account) é só o fallback quando o OAuth não está configurado, e
	// resulta no 403 storageQuotaExceeded conhecido. Sem GetBody o pipe não
	// é replayável, então d.do faz uma tentativa só — correto: reenviar
	// metade de um upload seria pior que falhar.
	client := d.uploadStream
	if client == nil {
		client = d.stream
	}
	resp, err := d.do(client, req)
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
