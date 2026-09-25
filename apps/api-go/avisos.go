package main

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/santos-tech/auth/db"
)

// Avisos da conta — para onde vai o aviso quando algo é de alguém.
//
// Nasceu do retorno ao cliente do bot do WhatsApp (bot-go reativacao.go): no
// dia em que um cliente pediu para ser chamado, o responsável tem que ficar
// sabendo. O bot já criava uma Tarefa, mas a Tarefa criada pela própria conta
// não notifica quem a criou (handlers_tasks.go) — e no caso padrão a conta que
// cria e a que recebe são a mesma. POST /avisos é o caminho direto: sino, push
// e, se pedido, e-mail.
//
// Cada conta escolhe os contatos em Configurações → Avisos:
//   aviso_email    — NULL = o e-mail de login;
//   aviso_telefone — NULL = o bot avisa os administradores, como antes.

const (
	avisoTituloMax = 120
	avisoCorpoMax  = 4000
)

// normalizaAvisoEmail devolve o e-mail em minúsculas. "" = limpar (volta ao de login).
func normalizaAvisoEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	if len(s) > 254 {
		return "", errors.New("e-mail longo demais")
	}
	a, err := mail.ParseAddress(s)
	// Só o endereço: "Nome <x@y>" é aceito pelo ParseAddress e não queremos.
	if err != nil || a.Address != s || !strings.Contains(s[strings.LastIndex(s, "@"):], ".") {
		return "", errors.New("e-mail inválido")
	}
	return s, nil
}

// normalizaAvisoTelefone devolve só os dígitos (com DDI). "" = limpar.
// Aceita a pontuação comum (+, espaço, parênteses, hífen); qualquer outra coisa é erro.
func normalizaAvisoTelefone(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune("+ ()-.", r):
		default:
			return "", errors.New("telefone inválido: use só números, com DDI e DDD")
		}
	}
	d := b.String()
	// Sem DDI (DDD + número, 10 ou 11 dígitos) é número do Brasil: entra o 55.
	// Com "+" na frente, a pessoa já escreveu o DDI que quis — não mexe.
	if !strings.HasPrefix(s, "+") && (len(d) == 10 || len(d) == 11) {
		d = "55" + d
	}
	if len(d) < 10 || len(d) > 15 {
		return "", errors.New("telefone inválido: use DDI + DDD + número (ex.: 5516999990000)")
	}
	return d, nil
}

type avisoInput struct {
	UserID int64  `json:"userId"`
	Titulo string `json:"titulo"`
	Corpo  string `json:"corpo"`
	URL    string `json:"url"`
	Email  bool   `json:"email"`
}

// validaAvisoInput — o link vai para o push e para o e-mail de alguém, então
// só aceita caminho interno ou https do próprio domínio (nada de phishing
// saindo com a cara da plataforma).
func validaAvisoInput(in avisoInput) error {
	if in.UserID <= 0 {
		return errors.New("userId obrigatório")
	}
	if strings.TrimSpace(in.Titulo) == "" || len([]rune(in.Titulo)) > avisoTituloMax {
		return fmt.Errorf("título obrigatório, até %d caracteres", avisoTituloMax)
	}
	if len([]rune(in.Corpo)) > avisoCorpoMax {
		return fmt.Errorf("corpo até %d caracteres", avisoCorpoMax)
	}
	if in.URL != "" && !linkDeAvisoValido(in.URL) {
		return errors.New("link precisa ser da plataforma")
	}
	return nil
}

func linkDeAvisoValido(raw string) bool {
	if strings.HasPrefix(raw, "/") {
		return !strings.HasPrefix(raw, "//")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "santos-tech.com" || strings.HasSuffix(h, ".santos-tech.com")
}

// emailDeAviso — HTML simples, com tudo escapado: o corpo pode trazer texto do cliente.
func emailDeAviso(titulo, corpo, link string) string {
	var b strings.Builder
	b.WriteString(`<div style="font-family:sans-serif;max-width:560px;color:#212121">`)
	fmt.Fprintf(&b, `<h2 style="color:#0E2937">%s</h2>`, html.EscapeString(titulo))
	if corpo != "" {
		fmt.Fprintf(&b, `<p style="line-height:1.5">%s</p>`, strings.ReplaceAll(html.EscapeString(corpo), "\n", "<br>"))
	}
	if link != "" {
		fmt.Fprintf(&b, `<p><a href="%s" style="background:#0DB88F;color:#fff;padding:10px 16px;border-radius:8px;text-decoration:none">Abrir na plataforma</a></p>`,
			html.EscapeString(link))
	}
	b.WriteString(`<p style="color:#496B84;font-size:12px">Para mudar para onde vão estes avisos: Configurações da conta → Avisos.</p></div>`)
	return b.String()
}

func avisosJSON(email string, avisoEmail, avisoTelefone *string) map[string]string {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return map[string]string{"emailLogin": email, "avisoEmail": deref(avisoEmail), "avisoTelefone": deref(avisoTelefone)}
}

func strPtrOuNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// GET /auth/me/avisos
func (s *Server) handleGetMeAvisos(w http.ResponseWriter, r *http.Request) {
	row, err := s.q.GetUserAvisos(r.Context(), int32(userIDFrom(r)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Conta não encontrada"))
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, avisosJSON(row.Email, row.AvisoEmail, row.AvisoTelefone))
}

// PATCH /auth/me/avisos — campo ausente preserva; "" limpa (volta ao padrão).
func (s *Server) handlePatchMeAvisos(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AvisoEmail    *string `json:"avisoEmail"`
		AvisoTelefone *string `json:"avisoTelefone"`
	}
	if err := decodeJSONLimit(r, &in, 4<<10); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	p := db.UpdateUserAvisosParams{ID: int32(userIDFrom(r))}
	if in.AvisoEmail != nil {
		v, err := normalizaAvisoEmail(*in.AvisoEmail)
		if err != nil {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", err.Error()))
			return
		}
		p.SetEmail, p.AvisoEmail = true, strPtrOuNil(v)
	}
	if in.AvisoTelefone != nil {
		v, err := normalizaAvisoTelefone(*in.AvisoTelefone)
		if err != nil {
			writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", err.Error()))
			return
		}
		p.SetTelefone, p.AvisoTelefone = true, strPtrOuNil(v)
	}
	row, err := s.q.UpdateUserAvisos(r.Context(), p)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Conta não encontrada"))
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, avisosJSON(row.Email, row.AvisoEmail, row.AvisoTelefone))
}

// POST /avisos (admin) — avisa uma conta: sino + push e, com email:true, e-mail
// para o contato de aviso dela. Devolve o telefone de aviso para quem chamou
// (o bot) mandar o WhatsApp; "" = a conta não cadastrou.
func (s *Server) handleCreateAviso(w http.ResponseWriter, r *http.Request) {
	var in avisoInput
	if err := decodeJSONLimit(r, &in, 16<<10); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validaAvisoInput(in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "VALIDATION_ERROR", err.Error()))
		return
	}
	row, err := s.q.GetUserAvisos(r.Context(), int32(in.UserID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, appErr(http.StatusNotFound, "NOT_FOUND", "Conta não encontrada"))
			return
		}
		writeErr(w, err)
		return
	}
	s.notifyUser(r.Context(), int32(in.UserID), in.Titulo, in.Corpo, in.URL)

	emailPara := ""
	if in.Email {
		emailPara = row.Email
		if row.AvisoEmail != nil && *row.AvisoEmail != "" {
			emailPara = *row.AvisoEmail
		}
		s.enqueueEmail("aviso", emailPara, in.Titulo, emailDeAviso(in.Titulo, in.Corpo, in.URL))
	}
	telefone := ""
	if row.AvisoTelefone != nil {
		telefone = *row.AvisoTelefone
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "telefone": telefone, "emailEnviado": emailPara != ""})
}
