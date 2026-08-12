package main

import "testing"

func TestVerifyContent_RejectsBareKeywordName(t *testing.T) {
	content := `## Env vars\nUse DOTFY_API_KEY to authenticate against the payments API.`
	has, masked, live := verifyContent("DOTFY_API_KEY", content)
	if has {
		t.Fatalf("esperava rejeitar menção sem valor, mas achou candidato: masked=%q live=%q", masked, live)
	}
}

func TestVerifyContent_RejectsSelfReferencingCodeAccessor(t *testing.T) {
	// casos reais que escapou: código acessando a env var de um jeito que
	// não é literalmente "process.env.X" mas ainda assim é código, não valor.
	cases := []string{
		`SENTRY_API_TOKEN: runtimeEnv.SENTRY_API_TOKEN,`,
		`token = env.local.get_sentry_api_token()`,
	}
	for _, content := range cases {
		has, masked, _ := verifyContent("SENTRY_API_TOKEN", content)
		if has {
			t.Fatalf("esperava rejeitar auto-referência %q, mas aceitou (masked=%q)", content, masked)
		}
	}
}

func TestVerifyContent_RejectsCdkSecretValueUnsafePlainText(t *testing.T) {
	content := `SENTRY_API_TOKEN: cdk.SecretValue.unsafePlainText(process.env.SENTRY_API_TOKEN!).unsafeUnwrap(),`
	has, masked, _ := verifyContent("SENTRY_API_TOKEN", content)
	if has {
		t.Fatalf("esperava rejeitar cdk.SecretValue.unsafePlainText, mas aceitou (masked=%q)", masked)
	}
}

func TestVerifyContent_RejectsPlaceholderValue(t *testing.T) {
	content := `DOTFY_API_KEY=process.env.DOTFY_API_KEY`
	has, _, _ := verifyContent("DOTFY_API_KEY", content)
	if has {
		t.Fatalf("esperava rejeitar placeholder process.env.*, mas marcou como candidato")
	}
}

func TestVerifyContent_RejectsPortuguesePlaceholder(t *testing.T) {
	cases := []string{
		`ABACATEPAY_API_KEY=sua_api_key_aqui`,
		`ABACATEPAY_API_KEY="coloque_sua_chave_aqui"`,
		`ABACATEPAY_API_KEY=chave_de_teste_exemplo`,
	}
	for _, content := range cases {
		has, masked, _ := verifyContent("ABACATEPAY_API_KEY", content)
		if has {
			t.Fatalf("esperava rejeitar placeholder em português %q, mas aceitou (masked=%q)", content, masked)
		}
	}
}

func TestVerifyContent_RejectsAquiAsWholeWord(t *testing.T) {
	// caso real que escapou: \b não separa "_" de letra em regex, então
	// "abc_dev_XXXXXXXX_aqui" nunca batia no antigo \baqui\b.
	content := `ABACATEPAY_API_KEY=abc_dev_9f7Hk2wQmZ_aqui`
	has, masked, _ := verifyContent("ABACATEPAY_API_KEY", content)
	if has {
		t.Fatalf("esperava rejeitar valor terminado em _aqui, mas aceitou (masked=%q)", masked)
	}
}

func TestVerifyContent_AcceptsValueContainingAquiAsSubstringNotToken(t *testing.T) {
	// "aquiles123456789secretkey" não deveria ser rejeitado só porque começa
	// com as letras "aqui" — não é o token "aqui" isolado.
	content := `ABACATEPAY_API_KEY=aquiles123456789secretkeyvalue`
	has, _, _ := verifyContent("ABACATEPAY_API_KEY", content)
	if !has {
		t.Fatalf("esperava aceitar valor que só contém 'aqui' como prefixo de outra palavra, não como token isolado")
	}
}

func TestVerifyContent_DoesNotCrossLineIntoNextVar(t *testing.T) {
	// caso real que escapou: "API_KEY=" seguido de quebra de linha (valor
	// vazio, arquivo só documentando as env vars) fazia o \s* atravessar a
	// linha e capturar o NOME da próxima variável como se fosse o valor.
	content := "SUPABASE_SERVICE_ROLE_KEY=\nABACATEPAY_API_KEY=\nABACATEPAY_WEBHOOK_SECRET=\nABACATEPAY_HMAC_PUBLIC_KEY=\n"
	has, masked, _ := verifyContent("ABACATEPAY_API_KEY", content)
	if has {
		t.Fatalf("esperava rejeitar valor vazio seguido de outra var, mas capturou (masked=%q)", masked)
	}
}

func TestVerifyContent_RejectsAquiGluedWithoutSeparator(t *testing.T) {
	// caso real que escapou: "abc_dev_SUACHAVEAQUI" — "sua"+"chave"+"aqui"
	// colados sem separador, o split por token não enxerga isso.
	content := `ABACATEPAY_API_KEY=abc_dev_SUACHAVEAQUI`
	has, masked, _ := verifyContent("ABACATEPAY_API_KEY", content)
	if has {
		t.Fatalf("esperava rejeitar abc_dev_SUACHAVEAQUI, mas aceitou (masked=%q)", masked)
	}
}

func TestVerifyContent_RejectsYourPrefixedPlaceholder(t *testing.T) {
	// caso real que escapou: "your_abacatepay_api_key" não batia no regex
	// antigo porque exigia "your" colado direto em key/api/token.
	content := `ABACATEPAY_API_KEY=your_abacatepay_api_key`
	has, masked, _ := verifyContent("ABACATEPAY_API_KEY", content)
	if has {
		t.Fatalf("esperava rejeitar your_<qualquer coisa>, mas aceitou (masked=%q)", masked)
	}
}

func TestVerifyContent_AcceptsRealLookingAssignment(t *testing.T) {
	content := `DOTFY_API_KEY=aB3xQ9zL7mK2pR8sT1vW4yC6nH0jF5dG`
	has, masked, live := verifyContent("DOTFY_API_KEY", content)
	if !has {
		t.Fatalf("esperava aceitar valor real, mas rejeitou")
	}
	if masked == "" {
		t.Fatalf("esperava masked preenchido")
	}
	if live != "" {
		t.Fatalf("valor não está no formato vk_(test|live)_, live-candidate deveria ficar vazio, veio %q", live)
	}
}

func TestVerifyContent_VkKeyIsLiveCandidate(t *testing.T) {
	content := `Authorization: Bearer vk_test_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789AbCdEfGh`
	has, masked, live := verifyContent("vk_test_", content)
	if !has {
		t.Fatalf("esperava aceitar chave vk_test_ válida no formato")
	}
	if live == "" {
		t.Fatalf("esperava live-candidate preenchido pra chave vk_test_, masked=%q", masked)
	}
}

func TestVerifyContent_RejectsLowEntropyValue(t *testing.T) {
	content := `DOTFY_API_KEY=aaaaaaaaaaaaaaaaaaaa`
	has, _, _ := verifyContent("DOTFY_API_KEY", content)
	if has {
		t.Fatalf("esperava rejeitar valor de baixa entropia (repetido)")
	}
}

func TestIsExamplePath(t *testing.T) {
	cases := map[string]bool{
		".env.example":           true,
		".env.hostinger.example": true, // caso real que escapou do marker literal ".env.example"
		"config/.env.sample":     true,
		"docs/.env.template.md":  true,
		".env":                   false,
		"src/env.ts":             false,
		"payments-go-pix.md":     false,
	}
	for path, want := range cases {
		if got := isExamplePath(path); got != want {
			t.Errorf("isExamplePath(%q) = %v, esperava %v", path, got, want)
		}
	}
}

func TestIsOwnRepo(t *testing.T) {
	if !isOwnRepo("Guilhermeb-Ferrarezi/santos-tech-infra") {
		t.Fatalf("esperava reconhecer Guilhermeb-Ferrarezi como dono próprio")
	}
	if isOwnRepo("someone-else/random-repo") {
		t.Fatalf("não deveria reconhecer repo de terceiro como próprio")
	}
}
