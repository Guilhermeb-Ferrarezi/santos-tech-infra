package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

// O espelho dos dossiês no Google Drive.
//
// POR QUE ESPELHO, E NÃO A FONTE. O banco continua sendo a verdade: é dele que
// o bot lê a cada mensagem, e ler do Drive nessa frequência custaria latência,
// cota de API e um ponto de falha novo entre o cliente e a resposta. O Drive é
// onde a COORDENAÇÃO lê — no celular, antes de ligar para a família.
//
// POR QUE O BOT CRIA A PRÓPRIA PASTA. O escopo é drive.file, que dá acesso só
// aos arquivos que o próprio app criou. Ele não enxerga nem toca em mais nada
// do Drive de quem autorizou. A contrapartida é que ele também não consegue
// escrever numa pasta feita à mão — então cria a dele, e essa pasta aparece no
// Drive de quem autorizou, pronta para ser compartilhada com a equipe.

const nomeDaPastaDeAtendimentos = "Atendimentos — Santos Tech"

// GarantePasta devolve o id da pasta do bot, criando-a na primeira vez.
func (g *GCalClient) GarantePasta(ctx context.Context, refreshToken string) (string, error) {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return "", err
	}

	// Procura antes de criar: sem isto, cada reinício do bot criaria mais uma
	// pasta com o mesmo nome, e o Drive permite isso sem reclamar.
	q := url.Values{}
	q.Set("q", fmt.Sprintf("name = %q and mimeType = 'application/vnd.google-apps.folder' and trashed = false",
		nomeDaPastaDeAtendimentos))
	q.Set("fields", "files(id,name)")
	q.Set("pageSize", "10")
	var busca struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := g.doJSON(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files?"+q.Encode(), access, nil, &busca); err != nil {
		return "", fmt.Errorf("drive: busca da pasta: %w", err)
	}
	if len(busca.Files) > 0 {
		return busca.Files[0].ID, nil
	}

	corpo, err := json.Marshal(map[string]any{
		"name":     nomeDaPastaDeAtendimentos,
		"mimeType": "application/vnd.google-apps.folder",
	})
	if err != nil {
		return "", err
	}
	var criada struct {
		ID string `json:"id"`
	}
	if err := g.doJSON(ctx, http.MethodPost,
		"https://www.googleapis.com/drive/v3/files?fields=id", access, corpo, &criada); err != nil {
		return "", fmt.Errorf("drive: criar a pasta: %w", err)
	}
	g.log.Info("drive: pasta de atendimentos criada", "id", criada.ID, "nome", nomeDaPastaDeAtendimentos)
	return criada.ID, nil
}

// EscreveDossie grava (ou reescreve) o documento de um cliente.
//
// Procura pelo telefone no nome do arquivo em vez de guardar o id: um id salvo
// no banco vira lixo quando alguém apaga o arquivo à mão, e aí o bot passa a
// reescrever um arquivo que não existe mais, em silêncio. O nome é a chave
// porque é o que sobrevive a alguém mexer na pasta.
func (g *GCalClient) EscreveDossie(ctx context.Context, refreshToken, pastaID, telefone, nomeArquivo, conteudo string) error {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("q", fmt.Sprintf("name contains %q and %q in parents and trashed = false", telefone, pastaID))
	q.Set("fields", "files(id,name)")
	q.Set("pageSize", "5")
	var busca struct {
		Files []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if err := g.doJSON(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files?"+q.Encode(), access, nil, &busca); err != nil {
		return fmt.Errorf("drive: busca do dossiê: %w", err)
	}

	metadados := map[string]any{"name": nomeArquivo}
	metodo, endpoint := http.MethodPost,
		"https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&fields=id"
	if len(busca.Files) > 0 {
		// Atualiza no lugar: o link que a coordenação salvou continua valendo.
		metodo = http.MethodPatch
		endpoint = "https://www.googleapis.com/upload/drive/v3/files/" +
			url.PathEscape(busca.Files[0].ID) + "?uploadType=multipart&fields=id"
	} else {
		metadados["parents"] = []string{pastaID}
	}

	corpo, tipo, err := multipartDrive(metadados, conteudo)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, metodo, endpoint, bytes.NewReader(corpo))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", tipo)
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("drive: gravar %s status %d: %s", nomeArquivo, resp.StatusCode, string(raw))
	}
	return nil
}

// multipartDrive monta o corpo que o Drive espera para upload com metadados:
// uma parte JSON com o nome/pasta e outra com o conteúdo do arquivo.
func multipartDrive(metadados map[string]any, conteudo string) ([]byte, string, error) {
	meta, err := json.Marshal(metadados)
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	cabJSON := textproto.MIMEHeader{}
	cabJSON.Set("Content-Type", "application/json; charset=UTF-8")
	parte, err := w.CreatePart(cabJSON)
	if err != nil {
		return nil, "", err
	}
	if _, err := parte.Write(meta); err != nil {
		return nil, "", err
	}

	cabMD := textproto.MIMEHeader{}
	cabMD.Set("Content-Type", "text/markdown; charset=UTF-8")
	parte, err = w.CreatePart(cabMD)
	if err != nil {
		return nil, "", err
	}
	if _, err := parte.Write([]byte(conteudo)); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	// O Drive exige "multipart/related", que o mime/multipart não escreve.
	return buf.Bytes(), "multipart/related; boundary=" + w.Boundary(), nil
}

// TemEscopoDoDrive diz se esta conta autorizou a escrita no Drive.
//
// Conta autorizada antes do escopo existir continua servindo para o Google
// Agenda e falharia no Drive; perguntar antes evita erro a cada aula marcada.
func (g *GCalClient) TemEscopoDoDrive(ctx context.Context, refreshToken string) bool {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return false
	}
	var info struct {
		Scope string `json:"scope"`
	}
	if err := g.doJSON(ctx, http.MethodGet,
		"https://www.googleapis.com/oauth2/v3/tokeninfo?access_token="+url.QueryEscape(access),
		access, nil, &info); err != nil {
		return false
	}
	return strings.Contains(info.Scope, driveScope)
}
