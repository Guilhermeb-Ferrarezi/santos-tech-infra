package main

import "strings"

// examplePathMarkers são sufixos/trechos de path que indicam arquivo de
// exemplo/template — por definição só tem placeholder, nunca segredo real.
// Genéricos de propósito: ".example" pega tanto ".env.example" quanto
// ".env.hostinger.example", ".env.staging.example", etc.
var examplePathMarkers = []string{
	".example",
	".sample",
	".template",
	".dist",
	"example.env",
	"sample.env",
}

// isExamplePath confere se o caminho do arquivo é claramente um template
// (não vale a pena nem baixar o conteúdo pra verificar).
func isExamplePath(path string) bool {
	lower := strings.ToLower(path)
	for _, marker := range examplePathMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
