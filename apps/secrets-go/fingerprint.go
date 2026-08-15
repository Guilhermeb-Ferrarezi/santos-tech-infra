package main

import "strings"

// providerMatch liga um prefixo conhecido a um rótulo pra UI e a uma
// "family" interna que escolhe qual verificador ativo usar (verifiers.go).
// family vazia = só fingerprint, sem verificação ativa disponível (AWS
// precisa do secret pair, Google API Key não tem endpoint universal, JWT
// não é uma chave estática, pk_/whsec_ da Stripe não autenticam sozinhas).
var providerPrefixes = []struct {
	prefix   string
	provider string
	family   string
}{
	{"vk_test_", "Dotfy (sandbox)", "dotfy"},
	{"vk_live_", "Dotfy (produção)", "dotfy"},
	{"abc_dev_", "AbacatePay (sandbox)", "abacatepay"},
	{"abc_live_", "AbacatePay (produção)", "abacatepay"},
	{"sk-ant-", "Anthropic (Claude)", "anthropic"}, // tem que vir ANTES de "sk-" (mais específico)
	{"sk-proj-", "OpenAI (chave de projeto)", "openai"},
	{"sk-", "OpenAI (formato antigo) ou compatível", "openai"},
	{"sk_live_", "Stripe (produção)", "stripe"},
	{"sk_test_", "Stripe (sandbox)", "stripe"},
	{"rk_live_", "Stripe (chave restrita)", "stripe"},
	{"pk_live_", "Stripe (chave pública, produção)", ""},
	{"pk_test_", "Stripe (chave pública, sandbox)", ""},
	{"whsec_", "Stripe (webhook secret)", ""},
	{"ghp_", "GitHub (personal access token)", "github"},
	{"gho_", "GitHub (OAuth token)", "github"},
	{"github_pat_", "GitHub (fine-grained PAT)", "github"},
	{"AKIA", "AWS (Access Key ID)", ""},
	{"ASIA", "AWS (Access Key temporária/STS)", ""},
	{"xoxb-", "Slack (bot token)", "slack"},
	{"xoxp-", "Slack (user token)", "slack"},
	{"xoxa-", "Slack (app token)", "slack"},
	{"AIza", "Google API Key", ""},
	{"SG.", "SendGrid", "sendgrid"},
	{"hf_", "Hugging Face", "huggingface"},
	{"r8_", "Replicate", "replicate"},
	{"npm_", "npm", "npm"},
	{"dop_v1_", "DigitalOcean", "digitalocean"},
	{"gsk_", "Groq", "groq"},
	{"APP_USR-", "Mercado Pago (produção)", "mercadopago"},
	{"eyJ", "JWT (token, não é chave de API estática)", ""},
}

// keywordFamilies cobre provedores cujo valor não tem prefixo público
// reconhecível (Mercado Pago sandbox, Asaas, Cloudflare, Pagar.me — que usa
// o MESMO prefixo sk_test_/sk_live_ da Stripe, então precisa desempatar
// pelo nome da keyword) ou onde confiar só no valor arriscaria falso
// positivo. Usado como respaldo pro verificador quando o fingerprint do
// valor não identificou nada (ou pra desambiguar Pagar.me x Stripe).
var keywordFamilies = []struct {
	marker   string // substring em maiúsculo procurada na keyword
	provider string
	family   string
}{
	{"PAGARME", "Pagar.me", "pagarme"},
	{"MERCADOPAGO", "Mercado Pago", "mercadopago"},
	{"MERCADO_PAGO", "Mercado Pago", "mercadopago"},
	{"ASAAS", "Asaas", "asaas"},
	{"CLOUDFLARE", "Cloudflare", "cloudflare"},
	{"ANTHROPIC", "Anthropic (Claude)", "anthropic"},
	{"CLAUDE", "Anthropic (Claude)", "anthropic"}, // var nomeada CLAUDE_API_KEY/CLAUDE_CODE_* em vez de ANTHROPIC_*
	{"HUGGINGFACE", "Hugging Face", "huggingface"},
	{"HUGGING_FACE", "Hugging Face", "huggingface"},
	{"DIGITALOCEAN", "DigitalOcean", "digitalocean"},
	{"DIGITAL_OCEAN", "DigitalOcean", "digitalocean"},
	{"VERCEL", "Vercel", "vercel"},
	{"NETLIFY", "Netlify", "netlify"},
	{"NPM_TOKEN", "npm", "npm"},
	{"HEROKU", "Heroku", "heroku"},
	{"DISCORD", "Discord", "discord"},
	{"MAILGUN", "Mailgun", "mailgun"},
	{"GROQ", "Groq", "groq"},
	{"MISTRAL", "Mistral", "mistral"},
	{"REPLICATE", "Replicate", "replicate"},
	{"ELEVENLABS", "ElevenLabs", "elevenlabs"},
	{"ELEVEN_LABS", "ElevenLabs", "elevenlabs"},
	{"COHERE", "Cohere", "cohere"},
	{"TELEGRAM", "Telegram", "telegram"},
	{"GEMINI", "Google Gemini", "gemini"},
	{"SQUARE", "Square", "square"},
	{"CLERK", "Clerk", "clerk"},
	{"RAILWAY", "Railway", "railway"},
	{"FIREBASE", "Firebase", "firebase"},
	{"OPENROUTER", "OpenRouter", "openrouter"},
	{"OPEN_ROUTER", "OpenRouter", "openrouter"},
	{"RESEND", "Resend", "resend"},
	{"NOTION", "Notion", "notion"},
	{"FIGMA", "Figma", "figma"},
	{"SENTRY", "Sentry", "sentry"},
	{"DATADOG", "Datadog", "datadog"},
	{"CIRCLECI", "CircleCI", "circleci"},
	{"CIRCLE_CI", "CircleCI", "circleci"},
	{"GITLAB", "GitLab", "gitlab"},
	{"TOGETHER", "Together AI", "together"},
	{"FIREWORKS", "Fireworks AI", "fireworks"},
	{"XAI", "xAI (Grok)", "xai"},
	{"GROK", "xAI (Grok)", "xai"},
	{"DEEPSEEK", "DeepSeek", "deepseek"},
	{"STABILITY", "Stability AI", "stability"},
	{"DEEPGRAM", "Deepgram", "deepgram"},
	{"ASSEMBLYAI", "AssemblyAI", "assemblyai"},
	{"ASSEMBLY_AI", "AssemblyAI", "assemblyai"},
}

// keywordFamily busca a keyword (não o valor) contra provedores conhecidos
// só pelo nome — cobre os casos em que o valor não tem prefixo confiável.
func keywordFamily(keyword string) (provider string, family string) {
	upper := strings.ToUpper(keyword)
	for _, k := range keywordFamilies {
		if strings.Contains(upper, k.marker) {
			return k.provider, k.family
		}
	}
	return "", ""
}

// fingerprintProvider tenta identificar o provedor pelo prefixo conhecido do
// valor. Retorna "" quando não reconhece nenhum padrão — o que é o caso
// comum e não significa nada de errado, só que não está no catálogo.
func fingerprintProvider(value string) string {
	provider, _ := fingerprintProviderFamily(value)
	return provider
}

// fingerprintProviderFamily retorna o rótulo pra UI e a family usada pra
// escolher o verificador ativo (verifiers.go). family == "" quando não há
// verificador disponível pra esse padrão.
func fingerprintProviderFamily(value string) (provider string, family string) {
	for _, p := range providerPrefixes {
		if strings.HasPrefix(value, p.prefix) {
			return p.provider, p.family
		}
	}
	return "", ""
}
