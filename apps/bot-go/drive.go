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
	"time"
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

const nomeDaPastaDeAtendimentos = "Atendimentos do bot no WhatsApp"

// mesesPT — como a pasta do mês se chama. Em português porque quem abre a
// pasta é a coordenação, não um sistema.
var mesesPT = [...]string{"", "Janeiro", "Fevereiro", "Março", "Abril", "Maio",
	"Junho", "Julho", "Agosto", "Setembro", "Outubro", "Novembro", "Dezembro"}

// NomeDaPastaDoMes — "Setembro 2026".
func NomeDaPastaDoMes(t time.Time) string {
	t = t.In(brLocation)
	return fmt.Sprintf("%s %d", mesesPT[int(t.Month())], t.Year())
}

// GarantePasta devolve o id da pasta raiz do bot, criando-a na primeira vez.
//
// O bot cria a própria pasta porque o escopo é drive.file: ele só alcança o
// que ele mesmo criou. A contrapartida é boa — depois de criada, ela pode ser
// movida para qualquer lugar do Drive que o bot continua alcançando, e ele
// continua sem enxergar mais nada.
func (g *GCalClient) GarantePasta(ctx context.Context, refreshToken string) (string, error) {
	return g.garantePastaEm(ctx, refreshToken, nomeDaPastaDeAtendimentos, "")
}

// GarantePastaDoMes devolve a subpasta do mês, criando-a quando o mês vira.
//
// Só é chamada quando um cliente NOVO aparece: cliente que já tem dossiê não
// muda de pasta, mesmo voltando meses depois. Ver EncontraDossie.
func (g *GCalClient) GarantePastaDoMes(ctx context.Context, refreshToken, raizID string, quando time.Time) (string, error) {
	return g.garantePastaEm(ctx, refreshToken, NomeDaPastaDoMes(quando), raizID)
}

// garantePastaEm procura antes de criar.
//
// Sem a busca, cada reinício do bot criaria mais uma pasta com o mesmo nome —
// o Drive permite isso sem reclamar, e a coordenação acabaria com cinco pastas
// "Setembro 2026" e os atendimentos espalhados entre elas.
func (g *GCalClient) garantePastaEm(ctx context.Context, refreshToken, nome, paiID string) (string, error) {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return "", err
	}
	filtro := fmt.Sprintf("name = %q and mimeType = 'application/vnd.google-apps.folder' and trashed = false", nome)
	if paiID != "" {
		filtro += fmt.Sprintf(" and %q in parents", paiID)
	}
	q := url.Values{}
	q.Set("q", filtro)
	q.Set("fields", "files(id)")
	q.Set("pageSize", "10")
	var busca struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := g.doJSON(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files?"+q.Encode(), access, nil, &busca); err != nil {
		return "", fmt.Errorf("drive: busca da pasta %q: %w", nome, err)
	}
	if len(busca.Files) > 0 {
		return busca.Files[0].ID, nil
	}

	meta := map[string]any{"name": nome, "mimeType": "application/vnd.google-apps.folder"}
	if paiID != "" {
		meta["parents"] = []string{paiID}
	}
	corpo, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	var criada struct {
		ID string `json:"id"`
	}
	if err := g.doJSON(ctx, http.MethodPost,
		"https://www.googleapis.com/drive/v3/files?fields=id", access, corpo, &criada); err != nil {
		return "", fmt.Errorf("drive: criar a pasta %q: %w", nome, err)
	}
	g.log.Info("drive: pasta criada", "nome", nome, "id", criada.ID)
	return criada.ID, nil
}

// EncontraDossie procura o arquivo de um telefone em QUALQUER pasta do bot.
//
// A busca é global de propósito. O arquivo do cliente mora na pasta do mês em
// que ele apareceu pela PRIMEIRA vez, e fica lá para sempre: quem foi atendido
// em setembro e volta em dezembro continua tendo um dossiê só, em "Setembro
// 2026", atualizado. Procurar só na pasta do mês corrente criaria um segundo
// arquivo para a mesma pessoa a cada volta — e a memória dela ficaria picotada
// entre pastas, que é o oposto de ter memória.
func (g *GCalClient) EncontraDossie(ctx context.Context, refreshToken, telefone string) (string, error) {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return "", err
	}
	q := url.Values{}
	q.Set("q", fmt.Sprintf("name contains %q and trashed = false", telefone))
	q.Set("fields", "files(id,name)")
	q.Set("pageSize", "5")
	var busca struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := g.doJSON(ctx, http.MethodGet,
		"https://www.googleapis.com/drive/v3/files?"+q.Encode(), access, nil, &busca); err != nil {
		return "", fmt.Errorf("drive: busca do dossiê: %w", err)
	}
	if len(busca.Files) == 0 {
		return "", nil // cliente novo
	}
	return busca.Files[0].ID, nil
}

// EscreveDossie grava ou atualiza o documento de um cliente.
//
// Cliente conhecido: atualiza o arquivo onde ele já está, sem mover de pasta —
// o link que a coordenação salvou continua valendo, e o histórico dele não se
// parte entre meses.
//
// Cliente novo: cria na pasta do mês corrente, criando a pasta se o mês virou.
func (g *GCalClient) EscreveDossie(ctx context.Context, refreshToken, raizID, telefone, nomeArquivo, conteudo string, agora time.Time) error {
	arquivoID, err := g.EncontraDossie(ctx, refreshToken, telefone)
	if err != nil {
		return err
	}

	metodo, endpoint := http.MethodPatch, ""
	metadados := map[string]any{"name": nomeArquivo}
	if arquivoID != "" {
		endpoint = "https://www.googleapis.com/upload/drive/v3/files/" +
			url.PathEscape(arquivoID) + "?uploadType=multipart&fields=id"
	} else {
		mes, err := g.GarantePastaDoMes(ctx, refreshToken, raizID, agora)
		if err != nil {
			return err
		}
		metodo = http.MethodPost
		endpoint = "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart&fields=id"
		metadados["parents"] = []string{mes}
	}

	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return err
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

// TemEscopoDoDrive diz se esta conta pode — e DEVE — ser usada para escrever.
//
// Confere duas coisas, e a segunda é a que importa:
//
//  1. tem drive.file? senão, é conta de agenda e não serve aqui;
//  2. tem drive COMPLETO? então RECUSA.
//
// O segundo caso não é hipótese: a conta da diretoria — onde moram os arquivos
// sensíveis da empresa — devolveu um token com drive completo, porque já tinha
// concedido esse acesso a este mesmo app no passado e o Google somou as
// permissões antigas ao pedido novo. O bot passaria a enxergar os 5 TB.
//
// Recusar trava o espelhamento até alguém reautorizar limpo. É o certo: o
// dossiê continua no banco e ninguém perde nada, enquanto operar com acesso
// de sobra sobre arquivo sensível não tem desfazer.
func (g *GCalClient) TemEscopoDoDrive(ctx context.Context, refreshToken string) bool {
	escopos, err := g.escoposDoToken(ctx, refreshToken)
	if err != nil {
		return false
	}
	temFile, temTudo := false, false
	for _, e := range escopos {
		switch e {
		case driveScope:
			temFile = true
		case "https://www.googleapis.com/auth/drive",
			"https://www.googleapis.com/auth/drive.readonly":
			temTudo = true
		}
	}
	if temTudo {
		g.log.Error("drive: token com acesso ao Drive INTEIRO; recusando usar",
			"esperado", driveScope,
			"acao", "revogue o app em myaccount.google.com/permissions e autorize de novo")
		return false
	}
	return temFile
}

// escoposDoToken pergunta ao Google o que este token realmente permite.
//
// O que foi PEDIDO e o que foi CONCEDIDO podem divergir — e divergiram. A
// única fonte confiável é o próprio Google.
func (g *GCalClient) escoposDoToken(ctx context.Context, refreshToken string) ([]string, error) {
	access, err := g.accessToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	var info struct {
		Scope string `json:"scope"`
	}
	if err := g.doJSON(ctx, http.MethodGet,
		"https://www.googleapis.com/oauth2/v3/tokeninfo?access_token="+url.QueryEscape(access),
		access, nil, &info); err != nil {
		return nil, err
	}
	return strings.Fields(info.Scope), nil
}
