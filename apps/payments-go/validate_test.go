package main

import "testing"

func TestOnlyDigits(t *testing.T) {
	if got := onlyDigits("123.456.789-01"); got != "12345678901" {
		t.Fatalf("esperava 12345678901, veio %s", got)
	}
	if got := onlyDigits("(16) 99999-8888"); got != "16999998888" {
		t.Fatalf("esperava 16999998888, veio %s", got)
	}
}

func TestValidCPF(t *testing.T) {
	if !validCPF("12345678901") {
		t.Fatal("11 dígitos distintos deveriam ser válidos")
	}
	if validCPF("123") {
		t.Fatal("menos de 11 dígitos deveria ser inválido")
	}
	if validCPF("11111111111") {
		t.Fatal("todos os dígitos iguais deveria ser inválido")
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
