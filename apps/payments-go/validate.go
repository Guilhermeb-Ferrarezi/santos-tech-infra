package main

import (
	"errors"
	"strings"
)

// validPeriodicities é o conjunto aceito para produtos/recorrências (espelha o CHECK
// de pay_recurrences.periodicity e o vocabulário repassado à Efí).
var validPeriodicities = map[string]bool{
	"SEMANAL": true, "MENSAL": true, "TRIMESTRAL": true, "SEMESTRAL": true, "ANUAL": true,
}

// recurringFieldsValid valida os campos de assinatura quando recurring=true:
// periodicity ∈ conjunto válido e due_day 1–28. Sem efeito quando recurring=false.
func recurringFieldsValid(p Product) error {
	if !p.Recurring {
		return nil
	}
	if !validPeriodicities[p.Periodicity] {
		return errors.New("periodicity inválida (SEMANAL|MENSAL|TRIMESTRAL|SEMESTRAL|ANUAL) para produto recorrente")
	}
	if p.DueDay == nil || *p.DueDay < 1 || *p.DueDay > 28 {
		return errors.New("dueDay deve estar entre 1 e 28 para produto recorrente")
	}
	return nil
}

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
	return recurringFieldsValid(p)
}

// productUpdateValid valida um update: o slug é imutável e NÃO é enviado, então não
// é exigido aqui (só name e priceCents).
func productUpdateValid(p Product) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name obrigatório")
	}
	if p.PriceCents <= 0 {
		return errors.New("priceCents deve ser > 0")
	}
	return recurringFieldsValid(p)
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

// cpfCheckDigit calcula um dígito verificador de CPF pelo módulo 11: soma os n
// primeiros dígitos com pesos decrescentes começando em n+1 e devolve o resto
// convencionado (10 e 11 viram 0).
func cpfCheckDigit(digits string, n int) byte {
	sum := 0
	for i := 0; i < n; i++ {
		sum += int(digits[i]-'0') * (n + 1 - i)
	}
	r := sum * 10 % 11
	if r >= 10 {
		r = 0
	}
	return byte('0' + r)
}

// validCPF: 11 dígitos (já normalizado), não todos iguais E com os dois dígitos
// verificadores corretos. Só contar dígitos deixava passar qualquer sequência de 11
// números — e o CPF é o que amarra o cliente às cobranças/assinaturas.
func validCPF(digits string) bool {
	if len(digits) != 11 {
		return false
	}
	allEqual := true
	for i := 1; i < len(digits); i++ {
		if digits[i] != digits[0] {
			allEqual = false
			break
		}
	}
	if allEqual {
		return false // todos iguais (000..., 111...) → inválido
	}
	return digits[9] == cpfCheckDigit(digits, 9) && digits[10] == cpfCheckDigit(digits, 10)
}

// validPhone: vazio (opcional) ou 10–11 dígitos (fixo/celular BR), já normalizado.
func validPhone(digits string) bool {
	if digits == "" {
		return true
	}
	return len(digits) == 10 || len(digits) == 11
}

// validName: não vazio, máximo 100 caracteres.
func validName(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && len(s) <= 100
}

// validEmail: formato básico x@y.z, máximo 254 caracteres.
func validEmail(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 254 {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 {
		return false
	}
	dot := strings.LastIndex(s[at:], ".")
	return dot > 1 && dot < len(s[at:])-1
}

// clientSafeGatewayMsg sanitiza a mensagem de erro do gateway antes de devolvê-la ao
// cliente: remove caracteres de controle e limita o tamanho. Mensagens anômalas
// (vazias, longas demais) caem num texto genérico para não vazar conteúdo inesperado.
func clientSafeGatewayMsg(raw string) string {
	s := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, raw)
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 200 {
		return "Cobrança recusada pelo gateway. Verifique os dados e tente novamente."
	}
	return s
}
