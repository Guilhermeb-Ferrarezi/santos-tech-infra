package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// Cadastro de aluno pelo painel — para os alunos que já estudam na escola mas
// nunca tiveram conta, porque entraram antes do portal existir.
//
// POR QUE UMA ROTA PRÓPRIA, e não o POST /auth/admin/users:
//
//  1. A SENHA. Todo aluno nasce com a mesma senha padrão, que o painel não pode
//     conhecer: o dashboard é JavaScript servido publicamente, e qualquer pessoa
//     que abra o arquivo leria a senha inicial de todos os alunos. Ela vem de
//     STUDENT_DEFAULT_PASSWORD, no ambiente do servidor, e não sai daqui.
//  2. O E-MAIL. É derivado do nome (joao.silva@santos-tech.com) e precisa
//     desviar de colisão. Fazer isso no navegador seria uma corrida: dois
//     cadastros simultâneos escolheriam o mesmo endereço e o segundo falharia.
//  3. SEM CONVITE. O portal do aluno ainda não foi liberado; mandar convite
//     agora levaria o aluno a uma porta que a escola não quer abrir.
//
// Devolve TAMBÉM o id do Portal, que é outro banco e outro id: é ele que a
// matrícula (`enrollment.user_id`) espera, e sem devolvê-lo o painel teria de
// procurar o aluno por e-mail logo depois de criá-lo.

/** Quantas variações de e-mail tentar antes de desistir: base, .002 … .999. */
const maxTentativasEmailAluno = 999

// localPartAluno monta a parte local a partir do nome: primeiro e último nome,
// separados por ponto, sem acento e em minúsculas.
//
// "José Gonçalves de Assunção" → "jose.assuncao". O meio do nome é descartado
// de propósito: é o que mantém o endereço curto e previsível para quem digita.
func localPartAluno(nome string) string {
	partes := strings.Fields(semAcento(strings.ToLower(strings.TrimSpace(nome))))
	limpas := make([]string, 0, len(partes))
	for _, p := range partes {
		if s := soLetrasENumeros(p); s != "" {
			limpas = append(limpas, s)
		}
	}
	switch len(limpas) {
	case 0:
		return ""
	case 1:
		return limpas[0]
	default:
		return limpas[0] + "." + limpas[len(limpas)-1]
	}
}

// semAcento troca os acentos do português pelas letras base. Uma tabela, e não
// normalização Unicode, porque o conjunto é pequeno, fechado e o resultado
// precisa ser sempre o mesmo — é o endereço de e-mail de uma pessoa.
func semAcento(s string) string {
	r := strings.NewReplacer(
		"á", "a", "à", "a", "ã", "a", "â", "a", "ä", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "õ", "o", "ô", "o", "ö", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ç", "c", "ñ", "n",
	)
	return r.Replace(s)
}

func soLetrasENumeros(s string) string {
	var b strings.Builder
	for _, c := range s {
		if unicode.IsLetter(c) && c < unicode.MaxASCII || unicode.IsDigit(c) {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// emailAlunoDisponivel acha o primeiro endereço livre para a base informada.
//
// A primeira pessoa fica com o endereço limpo; a partir da segunda entra o
// sufixo ".002", ".003" — com três dígitos porque assim a ordem alfabética
// continua batendo com a ordem de cadastro, o que não aconteceria com ".2" e
// ".10" lado a lado.
func (s *Server) emailAlunoDisponivel(ctx context.Context, base string) (string, error) {
	for i := 1; i <= maxTentativasEmailAluno; i++ {
		email := localPartTentativa(base, i) + "@" + staffDomain
		existing, err := s.userByEmail(ctx, email)
		if err != nil {
			return "", err
		}
		if existing == nil {
			return email, nil
		}
	}
	return "", conflictErr("não há endereço livre para este nome")
}

// localPartTentativa devolve a base na primeira tentativa e base.NNN a partir
// da segunda. Três dígitos porque assim a ordem alfabética continua batendo com
// a ordem de cadastro — com ".2" e ".10" lado a lado, não bateria.
func localPartTentativa(base string, tentativa int) string {
	if tentativa <= 1 {
		return base
	}
	return fmt.Sprintf("%s.%03d", base, tentativa)
}

type criarAlunoInput struct {
	Name string `json:"name"`
}

// POST /auth/admin/students — cria a conta do aluno e devolve os dois ids.
func (s *Server) handleCreateStudent(w http.ResponseWriter, r *http.Request) {
	var in criarAlunoInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	nome := strings.TrimSpace(in.Name)
	if nome == "" || len(nome) > 128 {
		writeErr(w, validationErr("nome é obrigatório (até 128 caracteres)"))
		return
	}
	base := localPartAluno(nome)
	if base == "" || !localPartRe.MatchString(base) {
		writeErr(w, validationErr("não foi possível montar um e-mail a partir deste nome"))
		return
	}
	// Falha explícita, e não uma senha fraca de emergência: sem a variável
	// configurada ninguém deve conseguir criar conta de aluno.
	if s.cfg.StudentDefaultPassword == "" {
		writeErr(w, appErr(http.StatusServiceUnavailable, "STUDENT_PASSWORD_UNSET",
			"defina STUDENT_DEFAULT_PASSWORD no servidor para cadastrar alunos"))
		return
	}

	email, err := s.emailAlunoDisponivel(r.Context(), base)
	if err != nil {
		writeErr(w, err)
		return
	}
	hash, err := hashPassword(s.cfg.StudentDefaultPassword)
	if err != nil {
		writeErr(w, err)
		return
	}
	u, err := s.insertUserWithRoleAndPassword(r.Context(), email, nome, hash, RoleStudent)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Sem convite, de propósito — ver o comentário no topo do arquivo.
	s.portalSyncUserBestEffort(r.Context(), u)
	s.provisionMailboxBestEffort(r.Context(), u)

	// O id do Portal é de outro banco. Se o sync falhou (é best-effort), sai
	// zero e o painel mostra o aluno criado mas não matriculado — melhor que
	// inventar um id e estourar uma FK na matrícula.
	portalID, err := s.portalUserIDByEmail(r.Context(), email)
	if err != nil {
		portalID = 0
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user":         adminUserJSON(u),
		"portalUserId": portalID,
	})
}
