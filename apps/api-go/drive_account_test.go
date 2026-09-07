package main

import (
	"net/http"
	"strings"
	"testing"
)

// A conta de upload é escolhida por pasta porque quem sobe o arquivo vira DONO
// dele no Drive — e só o dono recupera da lixeira. Estes testes travam a
// cascata de fallback, que é o que garante que a mudança seja aditiva: uma
// pasta marcada num ambiente sem o token novo continua subindo pela conta
// padrão em vez de quebrar.

func TestUploadClientCascata(t *testing.T) {
	padrao := &http.Client{}
	contratos := &http.Client{}
	sa := &http.Client{}

	casos := []struct {
		nome     string
		client   *DriveClient
		conta    string
		esperado *http.Client
	}{
		{
			nome:     "conta nomeada configurada é usada",
			client:   &DriveClient{stream: sa, uploadStream: padrao, uploadStreams: map[string]*http.Client{driveAccountContratos: contratos}},
			conta:    driveAccountContratos,
			esperado: contratos,
		},
		{
			nome:     "sem conta pedida vai na padrão",
			client:   &DriveClient{stream: sa, uploadStream: padrao, uploadStreams: map[string]*http.Client{driveAccountContratos: contratos}},
			conta:    "",
			esperado: padrao,
		},
		{
			nome:     "conta pedida sem token configurado cai na padrão, não quebra",
			client:   &DriveClient{stream: sa, uploadStream: padrao, uploadStreams: map[string]*http.Client{driveAccountContratos: nil}},
			conta:    driveAccountContratos,
			esperado: padrao,
		},
		{
			nome:     "conta desconhecida cai na padrão",
			client:   &DriveClient{stream: sa, uploadStream: padrao},
			conta:    "inexistente",
			esperado: padrao,
		},
		{
			nome:     "sem OAuth nenhum sobra a service account",
			client:   &DriveClient{stream: sa},
			conta:    driveAccountContratos,
			esperado: sa,
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if got := c.client.uploadClient(c.conta); got != c.esperado {
				t.Fatalf("client errado para conta %q", c.conta)
			}
		})
	}
}

func TestUploadClientNuncaDevolveNil(t *testing.T) {
	// A service account é o último recurso: mesmo sem OAuth nenhum, o upload
	// tem de chegar no Google e falhar com 403 storageQuotaExceeded — um nil
	// aqui viraria panic no meio do upload.
	d := &DriveClient{stream: &http.Client{}}
	for _, conta := range []string{"", driveAccountContratos, "qualquer"} {
		if d.uploadClient(conta) == nil {
			t.Fatalf("uploadClient(%q) devolveu nil", conta)
		}
	}
}

func TestValidacaoDaContaDeUpload(t *testing.T) {
	base := driveFolderInput{Name: "Contratos", DriveFolderID: "1AbCdEfGhIjKlMnOpQrStUvWxYz"}

	// Vazio tem de continuar válido: é o comportamento de antes do campo existir.
	if err := base.validate(); err != nil {
		t.Fatalf("conta vazia deveria ser válida: %v", err)
	}

	comConta := base
	comConta.UploadAccount = driveAccountContratos
	if err := comConta.validate(); err != nil {
		t.Fatalf("conta conhecida deveria ser válida: %v", err)
	}

	invalida := base
	invalida.UploadAccount = "conta-que-nao-existe"
	err := invalida.validate()
	if err == nil {
		t.Fatal("conta desconhecida deveria ser recusada")
	}
	if !strings.Contains(err.Error(), "conta de upload inválida") {
		t.Fatalf("mensagem inesperada: %v", err)
	}
}

// A migração é uma string única executada no boot; um teste de conteúdo guarda
// o statement contra remoção acidental sem precisar de banco.
func TestMigracaoTemColunaDaContaDeUpload(t *testing.T) {
	frag := "ALTER TABLE drive_folders ADD COLUMN IF NOT EXISTS upload_account TEXT NOT NULL DEFAULT ''"
	if !strings.Contains(migration, frag) {
		t.Fatal("a coluna upload_account sumiu da migração")
	}
}
