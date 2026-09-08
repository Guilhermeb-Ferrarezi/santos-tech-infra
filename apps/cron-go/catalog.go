package main

// CatalogAction é uma ação curada que um job pode disparar. Host/Path são fixos
// no código — o admin nunca digita URL no modo catálogo.
type CatalogAction struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Method string `json:"-"`
	Scheme string `json:"-"` // opcional; default "https" — use "http" apenas em testes
	Host   string `json:"-"`
	Path   string `json:"-"`
}

// Catalog é o registro curado. Ampliar aqui ao expor uma nova ação agendável.
var Catalog = map[string]CatalogAction{
	"payments.gerar-cobrancas-mes": {
		ID: "payments.gerar-cobrancas-mes", Label: "Gerar cobranças do mês",
		// Traefik roteia api.santos-tech.com/payments → payments-go (stripando /payments),
		// então a rota real lá é /internal/gerar-cobrancas. Idempotente (txid determinístico).
		Method: "POST", Host: "api.santos-tech.com", Path: "/payments/internal/gerar-cobrancas",
	},
}

func lookupCatalog(ref string) (CatalogAction, bool) {
	a, ok := Catalog[ref]
	return a, ok
}
