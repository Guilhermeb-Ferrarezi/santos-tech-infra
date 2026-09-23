package main

import (
	"fmt"
	"strings"
	"time"
)

// O dossiê do cliente em Markdown — um documento por pessoa.
//
// POR QUE GERADO, E NÃO GUARDADO. O banco já tem tudo: o dossiê estruturado e
// cada mensagem trocada. Manter um segundo arquivo em paralelo criaria o pior
// problema de todos — duas versões da verdade, divergindo em silêncio sempre
// que uma gravação falhasse. Gerar na hora significa que o documento nunca está
// desatualizado, porque ele não existe até alguém pedir.
//
// POR QUE EXISTE, JÁ QUE O BANCO TEM. Porque ninguém abre um banco de dados
// para saber quem é a família que vai chegar às 9h. O Markdown é a forma de
// LER: no Drive, no celular, colado numa mensagem para o professor. O banco é
// para o bot; o documento é para gente.
//
// O nome do arquivo é o TELEFONE, não o nome: telefone é o que não muda, e é
// por ele que se procura quando a pessoa volta seis meses depois.

// DossieCliente — tudo que se sabe de uma pessoa, pronto para virar documento.
type DossieCliente struct {
	Telefone       string
	Nome           string
	Qualificacao   Qualificacao
	PrimeiroTexto  time.Time
	UltimoTexto    time.Time
	TotalMensagens int
	Conversa       []LinhaDaConversa
	AulaEm         time.Time
	AulaTitulo     string
}

// LinhaDaConversa — uma mensagem, do jeito que uma pessoa lê.
type LinhaDaConversa struct {
	Quando time.Time
	DeQuem string // "cliente" | "bot"
	Texto  string
}

// Markdown escreve o documento do cliente.
func (d DossieCliente) Markdown(agora time.Time) string {
	var b strings.Builder
	q := d.Qualificacao

	nome := strings.TrimSpace(d.Nome)
	if nome == "" {
		nome = "(sem nome)"
	}
	fmt.Fprintf(&b, "# %s — %s\n\n", nome, FormataTelefone(d.Telefone))

	// ── cabeçalho: o que decide se vale a ligação ────────────────────────────
	fmt.Fprintf(&b, "> **%s**\n\n", q.Grau().Legivel())

	linha := func(rotulo, valor string) {
		if strings.TrimSpace(valor) != "" {
			fmt.Fprintf(&b, "- **%s:** %s\n", rotulo, valor)
		}
	}
	if !d.PrimeiroTexto.IsZero() {
		linha("Primeiro contato", d.PrimeiroTexto.In(brLocation).Format("02/01/2006"))
	}
	if !d.UltimoTexto.IsZero() {
		linha("Último contato", d.UltimoTexto.In(brLocation).Format("02/01/2006 15:04"))
	}
	if d.TotalMensagens > 0 {
		linha("Mensagens trocadas", fmt.Sprintf("%d", d.TotalMensagens))
	}
	if !d.AulaEm.IsZero() {
		quando := d.AulaEm.In(brLocation).Format("02/01/2006 às 15:04")
		if d.AulaEm.After(agora) {
			linha("Aula experimental", quando+" — **ainda vai acontecer**")
		} else {
			linha("Aula experimental", quando+" (já passou)")
		}
	}
	b.WriteString("\n")

	// ── quem é ────────────────────────────────────────────────────────────────
	b.WriteString("## Quem é\n\n")
	if q.Vazia() {
		b.WriteString("_Ainda não contou nada sobre si._\n\n")
	} else {
		switch q.ParaQuem {
		case "proprio":
			linha("O curso é", "para a própria pessoa")
		case "filho":
			linha("O curso é", "para um filho(a)")
		case "outro":
			linha("O curso é", "para outra pessoa da família")
		}
		linha("Aluno", q.AlunoNome)
		if q.AlunoIdade > 0 {
			linha("Idade", fmt.Sprintf("%d anos", q.AlunoIdade))
		}
		linha("Interesse", q.Interesse)
		switch q.JaFazCurso {
		case "sim":
			linha("Já faz curso livre", "sim")
		case "nao":
			linha("Já faz curso livre", "não")
		}
		linha("Disponibilidade", q.Disponibilidade)
		b.WriteString("\n")
	}

	// ── motivação: o que o vendedor usa primeiro ──────────────────────────────
	if q.Motivacao != "" || q.MotivacaoTipo != "" {
		b.WriteString("## Por que procurou\n\n")
		if q.Motivacao != "" {
			fmt.Fprintf(&b, "> %s\n\n", q.Motivacao)
		}
		if desc, ok := motivacoesValidas[q.MotivacaoTipo]; ok {
			fmt.Fprintf(&b, "_Em resumo: %s._\n\n", desc)
		}
	}

	if q.Observacoes != "" {
		b.WriteString("## Anotações\n\n")
		fmt.Fprintf(&b, "%s\n\n", q.Observacoes)
	}

	// ── o que já aconteceu ────────────────────────────────────────────────────
	b.WriteString("## Andamento\n\n")
	marca := func(feito bool, texto string) {
		if feito {
			fmt.Fprintf(&b, "- [x] %s\n", texto)
		} else {
			fmt.Fprintf(&b, "- [ ] %s\n", texto)
		}
	}
	marca(q.TurnosRespondendo > 0, "Conversou com a gente")
	marca(q.Respondidas() >= 3, fmt.Sprintf("Respondeu as perguntas (%d de %d)", q.Respondidas(), q.Respondidas()+len(q.Falta())))
	marca(q.PrecoInformado, "Soube os valores")
	marca(q.AulaMarcada, "Marcou a aula experimental")
	b.WriteString("\n")

	if falta := q.Falta(); len(falta) > 0 {
		b.WriteString("**Ainda não sabemos:**\n\n")
		for _, p := range falta {
			fmt.Fprintf(&b, "- %s\n", p.pergunta)
		}
		b.WriteString("\n")
	}

	// ── a conversa ────────────────────────────────────────────────────────────
	//
	// Vai por último e por inteiro. É o que ninguém consulta todo dia e o que
	// todo mundo quer quando precisa entender o que foi combinado.
	if len(d.Conversa) > 0 {
		b.WriteString("## Conversa\n\n")
		var diaAtual string
		for _, m := range d.Conversa {
			dia := m.Quando.In(brLocation).Format("02/01/2006")
			if dia != diaAtual {
				fmt.Fprintf(&b, "\n### %s\n\n", dia)
				diaAtual = dia
			}
			quem := "**Cliente**"
			if m.DeQuem != "cliente" {
				quem = "Marcos"
			}
			texto := strings.ReplaceAll(strings.TrimSpace(m.Texto), "\n", "  \n> ")
			fmt.Fprintf(&b, "`%s` %s\n> %s\n\n",
				m.Quando.In(brLocation).Format("15:04"), quem, texto)
		}
	}

	fmt.Fprintf(&b, "\n---\n_Gerado em %s a partir do banco do bot. Editar aqui NÃO muda o que o bot sabe._\n",
		agora.In(brLocation).Format("02/01/2006 15:04"))
	return b.String()
}

// FormataTelefone escreve o número do jeito que se lê no Brasil.
func FormataTelefone(e164 string) string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, e164)
	// 55 + DDD(2) + 9 dígitos
	if len(d) == 13 && strings.HasPrefix(d, "55") {
		return fmt.Sprintf("(%s) %s-%s", d[2:4], d[4:9], d[9:])
	}
	if len(d) == 12 && strings.HasPrefix(d, "55") {
		return fmt.Sprintf("(%s) %s-%s", d[2:4], d[4:8], d[8:])
	}
	return e164
}

// NomeDoArquivo — como o documento se chama no Drive.
//
// Telefone primeiro, porque é a chave que não muda e é por ela que se procura.
// O nome vem junto porque ninguém reconhece uma família por DDD.
func (d DossieCliente) NomeDoArquivo() string {
	nome := strings.TrimSpace(d.Nome)
	so := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ', r == '-':
			return r
		}
		return -1
	}, nome)
	so = strings.Join(strings.Fields(so), " ")
	if so == "" {
		return d.Telefone + ".md"
	}
	return d.Telefone + " - " + so + ".md"
}
