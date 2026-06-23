package main

// CatalogAction é uma ação curada que um job pode disparar. Host/Path são fixos
// no código — o admin nunca digita URL no modo catálogo.
type CatalogAction struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Method string `json:"-"`
	Host   string `json:"-"`
	Path   string `json:"-"`
}

// Catalog é o registro curado. Ampliar aqui ao expor uma nova ação agendável.
var Catalog = map[string]CatalogAction{
	"payments.gerar-cobrancas-mes": {
		ID: "payments.gerar-cobrancas-mes", Label: "Gerar cobranças do mês",
		Method: "POST", Host: "payments.santos-tech.com", Path: "/internal/gerar-cobrancas",
	},
	"email.relatorio-semanal": {
		ID: "email.relatorio-semanal", Label: "Enviar relatório semanal",
		Method: "POST", Host: "mails.santos-tech.com", Path: "/internal/relatorio-semanal",
	},
}

func lookupCatalog(ref string) (CatalogAction, bool) {
	a, ok := Catalog[ref]
	return a, ok
}
