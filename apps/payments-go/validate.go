package main

import (
	"errors"
	"strings"
)

func productValid(p Product) error {
	if strings.TrimSpace(p.Slug) == "" {
		return errors.New("slug obrigatório")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name obrigatório")
	}
	if p.PriceCents <= 0 {
		return errors.New("priceCents deve ser > 0")
	}
	return nil
}

// onlyDigits remove tudo que não for dígito (normaliza CPF/telefone vindos com máscara).
func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteByte(byte(r))
		}
	}
	return b.String()
}

// validCPF: exatamente 11 dígitos (já normalizado), e não todos iguais.
func validCPF(digits string) bool {
	if len(digits) != 11 {
		return false
	}
	for i := 1; i < len(digits); i++ {
		if digits[i] != digits[0] {
			return true
		}
	}
	return false // todos iguais (000..., 111...) → inválido
}

// validPhone: vazio (opcional) ou 10–11 dígitos (fixo/celular BR), já normalizado.
func validPhone(digits string) bool {
	if digits == "" {
		return true
	}
	return len(digits) == 10 || len(digits) == 11
}
