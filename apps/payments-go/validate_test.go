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
