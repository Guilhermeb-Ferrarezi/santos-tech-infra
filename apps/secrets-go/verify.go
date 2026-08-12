package main

import (
	"regexp"
	"strings"
	"unicode"
)

// strictKeyRegex conhece o formato exato da chave Dotfy (prefixo + 36 chars).
// Pra essas keywords, o match já É o valor — não precisa de extração adicional.
var strictKeyRegex = regexp.MustCompile(`vk_(test|live)_[A-Za-z0-9]{36}`)

// placeholderSubstrings são trechos que, se aparecerem em qualquer lugar do
// valor, indicam referência/template — não segredo real. Usa Contains (não
// precisa de word-boundary: "process.env." e "your_" já são específicos o
// bastante pra não colidir por acaso com um segredo de alta entropia).
var placeholderSubstrings = []string{
	"process.env", "import.meta.env", "os.getenv", "os.environ", "${",
	"runtimeenv.", "secretvalue.unsafeplaintext",
	"your_", "your-", "sua_", "sua-", "seu_", "seu-",
	"xxx", "changeme", "replace", "placeholder", "dummy", "fake",
	"redacted", "masked", "coloque", "insira", "preencha", "substitua",
	"tofake", "_fake",
}

// placeholderWords são palavras que só contam como placeholder quando
// aparecem como um TOKEN inteiro (separado por _ - . etc), não como
// substring — "aqui" pode ser parte de um hash real por acaso, mas o token
// "aqui" isolado (ex: "chave_de_teste_aqui") é claramente um exemplo. Regex
// com \b não serve aqui: \b não separa "_" de letra (ambos são \w).
var placeholderWords = map[string]struct{}{
	"aqui":    {},
	"exemplo": {},
	"example": {},
}

func looksLikePlaceholder(value string) bool {
	lower := strings.ToLower(value)

	if strings.Contains(lower, "<") && strings.Contains(lower, ">") {
		return true
	}
	if strings.Count(lower, "*") >= 4 {
		return true
	}
	// placeholder em português costuma terminar em "...aqui"/"...exemplo"
	// mesmo colado sem separador (ex: "abc_dev_SUACHAVEAQUI") — o split por
	// token abaixo não pega isso, então checa o sufixo direto.
	for _, suffix := range []string{"aqui", "exemplo", "example"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	for _, s := range placeholderSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}

	for _, word := range strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if _, ok := placeholderWords[word]; ok {
			return true
		}
	}
	return false
}

// assignmentRegex tenta capturar "<keyword> [:=] <valor>" — a diferença entre
// "o arquivo cita o NOME da variável" (não é vazamento) e "o arquivo tem o
// VALOR de verdade atribuído" (é vazamento).
func assignmentRegex(keyword string) *regexp.Regexp {
	escaped := regexp.QuoteMeta(keyword)
	// [ \t]* (não \s*) entre "=" e o valor: um "=" seguido de quebra de
	// linha (valor vazio, ex: "API_KEY=\nOUTRA_VAR=...") não pode deixar o
	// regex atravessar a linha e capturar o nome da PRÓXIMA variável.
	return regexp.MustCompile(`(?i)` + escaped + "[\"'`]?[ \t]*[:=][ \t]*[\"'`]?([A-Za-z0-9_\\-\\.\\/\\+]{16,})[\"'`]?")
}

// verifyContent confere o conteúdo real do arquivo (não a resposta "fuzzy" da
// Search API). Retorna:
//   - hasCandidate: achou algo que parece um segredo de verdade (não só o nome da var)
//   - value: o valor completo capturado, sem máscara — o achado já está
//     público no GitHub (um clique no link do arquivo mostra o mesmo), então
//     mascarar aqui só atrapalhava confirmar/revogar a chave.
//   - liveCandidate: valor cru no formato vk_(test|live)_..., só quando dá pra
//     tentar validar ao vivo contra a API da Dotfy (formato de chave conhecido)
func verifyContent(keyword, content string) (hasCandidate bool, value string, liveCandidate string) {
	if strictKeyRegex.MatchString(keyword) || keyword == "vk_test_" || keyword == "vk_live_" {
		m := strictKeyRegex.FindString(content)
		if m == "" {
			return false, "", ""
		}
		return true, m, m
	}

	m := assignmentRegex(keyword).FindStringSubmatch(content)
	if m == nil {
		return false, "", ""
	}
	val := m[1]
	if looksLikePlaceholder(val) || isLowEntropy(val) || referencesOwnKeyword(val, keyword) {
		return false, "", ""
	}

	// se o valor capturado por acaso bater no formato conhecido da Dotfy,
	// também vira candidato a verificação ao vivo.
	if strictKeyRegex.MatchString(val) {
		return true, val, strictKeyRegex.FindString(val)
	}
	return true, val, ""
}

// referencesOwnKeyword rejeita quando o "valor" capturado contém o próprio
// nome da keyword — sinal de que é código acessando a variável de outro
// jeito (runtimeEnv.SENTRY_API_TOKEN, env.local.get_sentry_api_token), não
// process.env especificamente, mas o mesmo problema: um segredo de verdade
// não costuma soletrar o próprio nome dentro do valor.
func referencesOwnKeyword(value, keyword string) bool {
	normalize := func(s string) string {
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, "-", "_")
		s = strings.ReplaceAll(s, ".", "_")
		return s
	}
	return strings.Contains(normalize(value), normalize(keyword))
}

// isLowEntropy rejeita valores óbvios demais pra serem segredo real (tipo
// "1111111111111111" ou "abcdefghijklmnopqrstuvwxyz").
func isLowEntropy(s string) bool {
	if len(s) == 0 {
		return true
	}
	distinct := map[byte]struct{}{}
	for i := 0; i < len(s); i++ {
		distinct[s[i]] = struct{}{}
	}
	return len(distinct) <= 3
}

// ownRepoOwners só marca o hit como "seu" (metadado exibido na UI) — não
// restringe mais a verificação ativa, que roda pra qualquer candidato.
var ownRepoOwners = []string{"guilhermeb-ferrarezi"}

func isOwnRepo(repoFullName string) bool {
	owner, _, found := strings.Cut(repoFullName, "/")
	if !found {
		return false
	}
	owner = strings.ToLower(owner)
	for _, allowed := range ownRepoOwners {
		if owner == allowed {
			return true
		}
	}
	return false
}
