package main

// Upload resumable do Google Drive (uploadType=resumable) — o caminho pra
// arquivos grandes demais pro multipart de UploadFileAs (teto 500MB, ver
// handlers_drive_folders.go): o professor grava aula particular em 1080p e o
// vídeo bruto pode passar de alguns GB. Aqui o navegador manda o arquivo em
// PEDAÇOS via PUT, retomável do ponto onde parou se a conexão cair no meio —
// ver handlers_drive_uploads.go pros três handlers HTTP que usam isto.
//
// A URI da sessão devolvida pelo Google (`sessionURI` abaixo) é uma credencial
// de ESCRITA direta no arquivo: quem a tem sobe bytes nele sem mais nenhuma
// autenticação. Por isso ela NUNCA sai do backend — fica só no registro da
// sessão (Redis, ver driveUploadSession), nunca na resposta HTTP pro
// navegador (que só recebe um `uploadId` opaco, gerado aqui).

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// driveResumableChunkSize: tamanho de pedaço sugerido ao front (devolvido
	// por handleCreateDriveUpload) — múltiplo de 256 KiB, exigência do Google
	// pra qualquer pedaço que não seja o último. 32MiB é grande o bastante pra
	// uma aula de 1-2h em 1080p (poucas dezenas de pedaços, não milhares) e
	// pequeno o bastante pra retomar sem perder muito trabalho numa queda.
	driveResumableChunkSize = 32 << 20 // 32 MiB

	// maxDriveResumableSize: teto de tamanho de arquivo aceito nesta rota —
	// bem acima dos ~2-6GB esperados de uma aula de 1-2h em 1080p, com folga
	// generosa. Acima disso o cliente deve seguir usando outra estratégia (não
	// é o caso hoje: o produto que motivou isto é justamente vídeo de aula).
	maxDriveResumableSize = 16 << 30 // 16 GiB

	// driveResumableSessionTTL: quanto tempo o REGISTRO da sessão (Redis)
	// sobrevive. A sessão do Google em si vale 1 semana, mas não faz sentido
	// reter aqui, além de um dia, uma credencial de escrita associada a um
	// envio que não terminou — se o usuário voltar depois disso, recomeça
	// criando uma sessão nova (a antiga no Google simplesmente expira sozinha).
	driveResumableSessionTTL = 24 * time.Hour
)

// driveUploadSession é o registro de uma sessão de upload resumable em
// andamento, guardado no Redis (chave driveUploadSessionKey) — efêmero de
// propósito, nunca no Postgres. SessionURI é a credencial de escrita (ver
// comentário no topo do arquivo): tudo que usa este struct existe pra nunca
// precisar repassá-la ao navegador.
type driveUploadSession struct {
	SessionURI string    `json:"sessionUri"`
	Account    string    `json:"account"`
	FolderID   string    `json:"folderId"` // uuid da NOSSA pasta (drive_folders.id) — não o id do Drive
	Size       int64     `json:"size"`
	MimeType   string    `json:"mimeType"`
	Name       string    `json:"name"`
	UserID     int64     `json:"userId"`
	CreatedAt  time.Time `json:"createdAt"`
}

func driveUploadSessionKey(uploadID string) string {
	return "drive:upload:" + uploadID
}

// isValidDriveUploadID reports whether s é um uploadId bem-formado — a saída
// exata de randomToken(32): 64 caracteres hex minúsculos. Mesma convenção de
// isValidChallenge/isValidResetToken (ver util.go): valida ANTES de montar a
// chave do Redis com entrada do path da requisição.
func isValidDriveUploadID(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// saveDriveUploadSession grava o registro da sessão. Ao contrário do
// cache-aside de cache.go (fail-open, é só um atalho pro banco), este É a
// fonte de verdade da sessão — um erro aqui tem que virar erro pro chamador,
// não ser engolido.
func (s *Server) saveDriveUploadSession(ctx context.Context, uploadID string, sess driveUploadSession) error {
	b, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
	defer cancel()
	return s.rdb.Set(rctx, driveUploadSessionKey(uploadID), b, driveResumableSessionTTL).Err()
}

// getDriveUploadSession devolve nil (sem erro) se a sessão não existe ou já
// expirou — mesma convenção de getDriveFolder pra "não encontrado".
func (s *Server) getDriveUploadSession(ctx context.Context, uploadID string) (*driveUploadSession, error) {
	rctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
	defer cancel()
	b, err := s.rdb.Get(rctx, driveUploadSessionKey(uploadID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sess driveUploadSession
	if err := json.Unmarshal(b, &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

// deleteDriveUploadSession apaga o registro — chamado quando o envio termina
// (done=true), quando a sessão do Google se perde (errResumableSessionLost,
// o front tem que recomeçar do zero de qualquer forma) ou quando o primeiro
// pedaço falha no sniff de Content-Type. Best-effort (cacheDel só loga erro):
// uma falha aqui não pode travar a resposta de sucesso/erro já decidida, e a
// entrada expira pelo TTL de qualquer forma.
func (s *Server) deleteDriveUploadSession(uploadID string) {
	s.cacheDel(driveUploadSessionKey(uploadID))
}

// contentRange é um "Content-Range: bytes <start>-<end>/<total>" já
// interpretado — o formato que o NAVEGADOR manda pra descrever um pedaço.
type contentRange struct {
	Start, End, Total int64
}

// parseContentRange interpreta o header Content-Range de um PUT de pedaço.
// ok=false para qualquer coisa malformada: sem o prefixo "bytes ", partes não
// numéricas, end < start ou total <= end. Não confere contra o tamanho da
// sessão nem contra Content-Length — isso é responsabilidade do handler
// (mistura validação de formato com validação de negócio deixaria a função
// pura mais difícil de testar isoladamente).
func parseContentRange(header string) (cr contentRange, ok bool) {
	const prefix = "bytes "
	if !strings.HasPrefix(header, prefix) {
		return contentRange{}, false
	}
	rest := header[len(prefix):]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return contentRange{}, false
	}
	rangePart, totalPart := rest[:slash], rest[slash+1:]
	dash := strings.IndexByte(rangePart, '-')
	if dash < 0 {
		return contentRange{}, false
	}
	start, errStart := strconv.ParseInt(rangePart[:dash], 10, 64)
	end, errEnd := strconv.ParseInt(rangePart[dash+1:], 10, 64)
	total, errTotal := strconv.ParseInt(totalPart, 10, 64)
	if errStart != nil || errEnd != nil || errTotal != nil {
		return contentRange{}, false
	}
	if start < 0 || end < start || total <= end {
		return contentRange{}, false
	}
	return contentRange{Start: start, End: end, Total: total}, true
}

// parseGoogleRangeHeader interpreta o header `Range` que o Google devolve num
// 308 de upload resumable — formato "bytes=0-N" (note o "=", diferente do
// Content-Range que o CLIENTE manda). N+1 é quantos bytes o Google já tem.
// ok=false quando o header vem vazio (nenhum byte recebido ainda).
func parseGoogleRangeHeader(v string) (received int64, ok bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(v, prefix) {
		return 0, false
	}
	rest := v[len(prefix):]
	dash := strings.IndexByte(rest, '-')
	if dash < 0 {
		return 0, false
	}
	// Confere as duas metades mesmo só usando `end`: um header que não segue
	// o formato exato do Google (ex.: lixo antes do "-") não deveria virar um
	// `received` inventado.
	if _, err := strconv.ParseInt(rest[:dash], 10, 64); err != nil {
		return 0, false
	}
	end, err := strconv.ParseInt(rest[dash+1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return end + 1, true
}

// ResumableResult é o resultado comum de um PUT de pedaço e de uma consulta
// de status (por baixo do capô, os dois são o mesmo tipo de requisição ao
// Drive — ver resumablePut): Done=false com Received = quantos bytes o
// Google confirma ter recebido até agora (é daí que o front retoma); Done=true
// com o arquivo já pronto (a última chamada, seja ela um pedaço final ou uma
// consulta de status depois do envio já ter terminado).
type ResumableResult struct {
	Done     bool
	Received int64
	File     DriveFile
}

// errResumableSessionLost: a sessão do Google expirou ou sumiu (404/410) — é
// definitivo, não transitório (por isso NÃO é um driveRetryableStatus; PUT de
// pedaço também não é replayable, corpo vem do r.Body da requisição HTTP).
// O chamador (handler) tem que apagar o registro e mandar o front recomeçar
// criando uma sessão nova.
var errResumableSessionLost = errors.New("drive: sessão de upload resumable perdida (404/410)")

// StartResumableUploadAs abre uma sessão de upload resumable dentro de
// folderID e devolve a URI da sessão (header `Location` da resposta) — ver
// comentário no topo do arquivo sobre por que essa URI nunca vai pro
// navegador. `account` escolhe a conta Google que sobe o arquivo (dona dele
// no Drive), mesma regra de UploadFileAs — ver uploadClient.
func (d *DriveClient) StartResumableUploadAs(ctx context.Context, account, folderID, filename, contentType string, size int64) (string, error) {
	body, err := json.Marshal(map[string]any{"name": filename, "parents": []string{folderID}})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&supportsAllDrives=true&fields=id,name,mimeType,size,modifiedTime,iconLink",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", contentType)
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))

	resp, err := d.do(d.uploadClient(account), req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", driveAPIError(resp)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("drive api: sessão resumable sem header Location")
	}
	return loc, nil
}

// resumablePut é o miolo comum de PutResumableChunk e GetResumableStatus —
// ambos são um PUT pra sessionURI que só difere no Content-Range e no corpo
// (pedaço real vs. corpo vazio com "bytes */<total>" pra só perguntar o
// status). Interpreta a resposta do Google segundo o protocolo de upload
// resumable: 308 = "recebi até aqui, continue"; 200/201 = arquivo pronto;
// 404/410 = sessão perdida; qualquer outra coisa = erro genérico.
func (d *DriveClient) resumablePut(ctx context.Context, account, sessionURI, contentRangeHeader string, contentLength int64, body io.Reader) (ResumableResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURI, body)
	if err != nil {
		return ResumableResult{}, err
	}
	req.ContentLength = contentLength
	req.Header.Set("Content-Range", contentRangeHeader)

	resp, err := d.do(d.uploadClient(account), req)
	if err != nil {
		return ResumableResult{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 308:
		received, _ := parseGoogleRangeHeader(resp.Header.Get("Range"))
		return ResumableResult{Done: false, Received: received}, nil
	case http.StatusOK, http.StatusCreated:
		var f driveFileWire
		if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
			return ResumableResult{}, err
		}
		return ResumableResult{Done: true, File: f.toDriveFile()}, nil
	case http.StatusNotFound, http.StatusGone:
		return ResumableResult{}, errResumableSessionLost
	default:
		return ResumableResult{}, driveAPIError(resp)
	}
}

// PutResumableChunk envia o pedaço [start,end] (inclusive, de `total` bytes
// no total) pra sessionURI — body é o r.Body da requisição HTTP, streamado
// direto pro Google sem bufferizar o pedaço inteiro em memória. Sem retry
// automático (ver d.do: body vindo de r.Body não tem GetBody, não é
// replayable) — reenviar metade de um pedaço já parcialmente aceito seria
// pior que devolver erro e deixar o FRONT decidir (consultar status e
// retomar do que o Google já confirma ter).
func (d *DriveClient) PutResumableChunk(ctx context.Context, account, sessionURI string, start, end, total int64, body io.Reader) (ResumableResult, error) {
	length := end - start + 1
	rangeHeader := fmt.Sprintf("bytes %d-%d/%d", start, end, total)
	return d.resumablePut(ctx, account, sessionURI, rangeHeader, length, body)
}

// GetResumableStatus pergunta ao Drive quantos bytes ele já tem de uma sessão,
// sem mandar nenhum byte novo — protocolo do Google pra consultar status de
// upload resumable: PUT com corpo vazio e Content-Range "bytes */<total>".
// body=nil (não http.NoBody) de propósito: com body nil o net/http.Request
// fica com Body == nil, e d.do trata isso como replayable (ver comentário de
// do) — então esta chamada específica CONTINUA repetível em 429/5xx,
// diferente de PutResumableChunk (body vem de r.Body, nunca é replayable).
func (d *DriveClient) GetResumableStatus(ctx context.Context, account, sessionURI string, total int64) (ResumableResult, error) {
	rangeHeader := fmt.Sprintf("bytes */%d", total)
	return d.resumablePut(ctx, account, sessionURI, rangeHeader, 0, nil)
}
