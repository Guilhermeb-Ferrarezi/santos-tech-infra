package main

import (
	"strings"
	"testing"
)

func TestOnlyDigits(t *testing.T) {
	if got := onlyDigits("123.456.789-01"); got != "12345678901" {
		t.Fatalf("esperava 12345678901, veio %s", got)
	}
	if got := onlyDigits("(16) 99999-8888"); got != "16999998888" {
		t.Fatalf("esperava 16999998888, veio %s", got)
	}
}

func TestValidCPF(t *testing.T) {
	// CPFs com dígito verificador correto.
	for _, ok := range []string{"12345678909", "39053344705", "94271564656", "11144477735"} {
		if !validCPF(ok) {
			t.Fatalf("CPF válido %s foi recusado", ok)
		}
	}
	if validCPF("123") {
		t.Fatal("menos de 11 dígitos deveria ser inválido")
	}
	if validCPF("11111111111") {
		t.Fatal("todos os dígitos iguais deveria ser inválido")
	}
	// Regressão do IDOR: 11 dígitos quaisquer NÃO bastam — sem os dois dígitos
	// verificadores, qualquer sequência virava um "cliente" com CPF arbitrário.
	for _, bad := range []string{"12345678901", "12345678900", "39053344700", "00000000001"} {
		if validCPF(bad) {
			t.Fatalf("CPF com dígito verificador errado (%s) deveria ser inválido", bad)
		}
	}
	// Um dígito verificador certo e o outro errado também é inválido.
	if validCPF("12345678908") {
		t.Fatal("segundo dígito verificador errado deveria ser inválido")
	}
	if validCPF("1234567890a") {
		t.Fatal("caractere não numérico deveria ser inválido")
	}
}

func TestValidPhone(t *testing.T) {
	if !validPhone("") {
		t.Fatal("vazio (opcional) deveria ser válido")
	}
	if !validPhone("1699998888") {
		t.Fatal("10 dígitos (fixo) deveria ser válido")
	}
	if !validPhone("16999998888") {
		t.Fatal("11 dígitos (celular) deveria ser válido")
	}
	if validPhone("123") {
		t.Fatal("poucos dígitos deveria ser inválido")
	}
}

func TestValidName(t *testing.T) {
	if !validName("João da Silva") {
		t.Fatal("nome normal deveria ser válido")
	}
	if validName("") {
		t.Fatal("vazio deveria ser inválido")
	}
	if validName("   ") {
		t.Fatal("só espaços deveria ser inválido")
	}
	if validName(string(make([]byte, 101))) {
		t.Fatal("mais de 100 chars deveria ser inválido")
	}
}

func TestValidEmail(t *testing.T) {
	if !validEmail("joao@email.com") {
		t.Fatal("email válido deveria passar")
	}
	if !validEmail("a@b.co") {
		t.Fatal("email curto válido deveria passar")
	}
	if validEmail("") {
		t.Fatal("vazio deveria ser inválido")
	}
	if validEmail("semArroba") {
		t.Fatal("sem @ deveria ser inválido")
	}
	if validEmail("@semlocal.com") {
		t.Fatal("sem local-part deveria ser inválido")
	}
	if validEmail("sem@ponto") {
		t.Fatal("sem ponto no domínio deveria ser inválido")
	}
}

// TestValidProviderID: o id vem do path JÁ DESESCAPADO e é concatenado na URL da Efí
// e num cabeçalho de resposta. Barra, ".." e CR/LF têm de ser recusados.
func TestValidProviderID(t *testing.T) {
	for _, ok := range []string{"abc123", "a-b_c.d", "0", strings.Repeat("a", 64)} {
		if !validProviderID(ok) {
			t.Fatalf("id legítimo %q foi recusado", ok)
		}
	}
	ruins := []string{
		"",                      // vazio
		strings.Repeat("a", 65), // longo demais
		"../../v2/gn/saldo",     // escapa do caminho da API
		"a/b",                   // barra
		"a b",                   // espaço
		"a\r\nX-Injected: 1",    // injeção de cabeçalho
		"a?b=1",                 // query
		"a#frag",
		"a%2Fb",
		`a"b`,
	}
	for _, bad := range ruins {
		if validProviderID(bad) {
			t.Fatalf("id perigoso %q deveria ser recusado", bad)
		}
	}
}
