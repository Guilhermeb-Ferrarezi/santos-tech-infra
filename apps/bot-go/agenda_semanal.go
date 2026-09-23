package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A agenda real da escola é uma GRADE SEMANAL, não uma lista de compromissos
// com data.
//
// Cada linha do Notion diz "Quarta, 08:00 ~ 10:00, Renata" — sem data. É assim
// que a escola pensa a agenda, e a estrutura reflete isso: o aluno tem um
// horário na semana, e quem lê a grade sabe que aquele espaço está tomado.
//
// O código anterior assumia data-e-hora exata, o que não existe nessa base.
// Resultado: o bot lia 34 aulas e não enxergava horário nenhum — foi por isso
// que ofereceu quarta às 8h com a Renata já marcada nesse exato horário.
//
// CONSEQUÊNCIA DE DESENHO: uma linha ocupa aquele dia da semana TODA semana,
// mesmo quando na prática era aula única. Isso faz o bot recusar, de vez em
// quando, um horário que na verdade vagou. É o erro certo: recusar demais custa
// uma negociação; marcar em cima custa uma família chegando e não tendo sala.

// diasPT — como o Notion escreve o dia, na ordem de time.Weekday (domingo=0).
var diasPT = [...]string{"Domingo", "Segunda", "Terça", "Quarta", "Quinta", "Sexta", "Sábado"}

// DiaDaSemanaPT devolve o rótulo que a agenda usa para aquela data.
func DiaDaSemanaPT(t time.Time) string {
	return diasPT[int(t.Weekday())]
}

// IntervaloSemanal — um espaço ocupado na grade, em minutos desde a meia-noite.
type IntervaloSemanal struct {
	Dia    string // "Quarta"
	Inicio int    // 480 = 08:00
	Fim    int    // 600 = 10:00
}

// ParseIntervalo lê o campo Horário, que é texto livre digitado por gente.
//
// Os quatro formatos abaixo convivem na base hoje, todos escritos à mão:
//
//	08:00 ~ 10:00
//	08:00–09:00      (travessão)
//	16h00-17h00
//	13h00-15h00
//
// Aceitar os quatro é obrigação, não gentileza: recusar um formato faria o bot
// deixar de enxergar aulas de verdade e marcar em cima delas.
func ParseIntervalo(dia, horario string) (IntervaloSemanal, bool) {
	h := strings.TrimSpace(horario)
	if h == "" || strings.TrimSpace(dia) == "" {
		return IntervaloSemanal{}, false
	}

	// Normaliza os separadores de intervalo num só, e "h" em ":".
	for _, sep := range []string{"~", "–", "—", " a ", "às", "as"} {
		h = strings.ReplaceAll(h, sep, "-")
	}
	h = strings.ReplaceAll(h, "h", ":")
	h = strings.ReplaceAll(h, "H", ":")

	partes := strings.SplitN(h, "-", 2)
	if len(partes) != 2 {
		return IntervaloSemanal{}, false
	}
	ini, ok1 := minutosDeHora(partes[0])
	fim, ok2 := minutosDeHora(partes[1])
	if !ok1 || !ok2 || fim <= ini {
		return IntervaloSemanal{}, false
	}
	return IntervaloSemanal{Dia: strings.TrimSpace(dia), Inicio: ini, Fim: fim}, true
}

// minutosDeHora lê "08:00", "8:00", "16:", "19" → minutos desde a meia-noite.
func minutosDeHora(s string) (int, bool) {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), ":"))
	if s == "" {
		return 0, false
	}
	hh, mm := s, "0"
	if i := strings.Index(s, ":"); i >= 0 {
		hh, mm = s[:i], s[i+1:]
		if strings.TrimSpace(mm) == "" {
			mm = "0"
		}
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(hh))
	m, err2 := strconv.Atoi(strings.TrimSpace(mm))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// Ocupa diz se o intervalo cobre o horário pedido naquela data.
//
// Compara SOBREPOSIÇÃO, não igualdade: uma aula das 8h às 10h ocupa as 9h
// também, e é exatamente esse o caso que estava passando batido.
func (iv IntervaloSemanal) Ocupa(quando time.Time, dur time.Duration) bool {
	if !strings.EqualFold(iv.Dia, DiaDaSemanaPT(quando)) {
		return false
	}
	ini := quando.Hour()*60 + quando.Minute()
	fim := ini + int(dur.Minutes())
	return ini < iv.Fim && iv.Inicio < fim
}

// FormataIntervalo escreve o horário no formato que a escola usa.
//
// Segue o padrão com "~" porque é o mais comum na base (21 das 33 linhas). Um
// bot que escreve diferente de todo mundo faz a grade parecer remendada.
func FormataIntervalo(inicio time.Time, dur time.Duration) string {
	fim := inicio.Add(dur)
	return fmt.Sprintf("%02d:%02d ~ %02d:%02d",
		inicio.Hour(), inicio.Minute(), fim.Hour(), fim.Minute())
}

// ── o marcador do bot ────────────────────────────────────────────────────────

// MarcadorBot — prefixo de tudo que o bot cria na agenda.
//
// Não dá para usar "Aula experimental" como marcador: a escola JÁ escreve isso
// à mão. Há quatro linhas assim hoje ("Aula Experimental MARTINS", "21/09 Aula
// Experimental"), e uma trava baseada nesse texto faria a faxina diária
// arquivar aulas de verdade.
//
// O robô resolve: nenhum título da base começa com ele, é impossível digitar
// por acidente, e quem abre a grade vê na hora o que foi o bot que marcou.
const MarcadorBot = "🤖"

// TituloAulaBot monta o título da linha que o bot cria.
//
// A data entra no título porque a base NÃO TEM campo de data — é assim que a
// escola já faz quando a aula é pontual ("Renata (só este sábado 19/09)"). Sem
// ela, uma aula única viraria um bloqueio permanente naquele dia da semana, e
// ninguém saberia quando limpar.
func TituloAulaBot(aluno string, quando time.Time) string {
	aluno = strings.TrimSpace(aluno)
	if aluno == "" {
		aluno = "a confirmar"
	}
	return fmt.Sprintf("%s %02d/%02d Aula experimental — %s",
		MarcadorBot, quando.Day(), int(quando.Month()), aluno)
}

// EhDoBot diz se o bot pode mexer nesta linha.
func EhDoBot(titulo string) bool {
	return strings.HasPrefix(strings.TrimSpace(titulo), MarcadorBot)
}

// DataNoTitulo extrai o "23/09" que o bot escreveu, para saber se a aula passou.
//
// Só olha títulos do bot: o formato é garantido porque foi ele que escreveu.
// Ler data de título escrito à mão seria adivinhação.
func DataNoTitulo(titulo string, agora time.Time) (time.Time, bool) {
	if !EhDoBot(titulo) {
		return time.Time{}, false
	}
	campos := strings.Fields(strings.TrimPrefix(strings.TrimSpace(titulo), MarcadorBot))
	if len(campos) == 0 {
		return time.Time{}, false
	}
	dm := strings.SplitN(campos[0], "/", 2)
	if len(dm) != 2 {
		return time.Time{}, false
	}
	d, err1 := strconv.Atoi(dm[0])
	m, err2 := strconv.Atoi(dm[1])
	if err1 != nil || err2 != nil || d < 1 || d > 31 || m < 1 || m > 12 {
		return time.Time{}, false
	}
	// Sem ano no título: assume o ano corrente, e o anterior quando isso
	// colocaria a aula mais de seis meses no futuro (vira do ano).
	ano := agora.Year()
	data := time.Date(ano, time.Month(m), d, 0, 0, 0, 0, brLocation)
	if data.Sub(agora) > 180*24*time.Hour {
		data = data.AddDate(-1, 0, 0)
	}
	return data, true
}

// NomeDoAluno escolhe o nome que vai para a agenda e para o Notion.
//
// O modelo às vezes escreve uma FRASE no lugar do nome — apareceu na agenda da
// escola como "Aula experimental — Não informado (filho do responsável)". Quem
// abre a grade quer ler um nome; quando ele não existe, o nome do responsável
// serve melhor que uma explicação, porque é por ele que a escola vai chamar na
// recepção.
// A detecção olha o COMEÇO do texto, não procura palavra solta no meio.
//
// A primeira versão barrava qualquer coisa que contivesse "filho" ou
// "responsável" em qualquer posição, e isso fazia duas coisas ruins:
//
//   - "Filho" é sobrenome de gente ("Antônio Barbosa Filho"). O aluno perdia o
//     nome e virava o responsável na agenda.
//   - Numa família com DOIS filhos, "filho mais novo" e "filho mais velho"
//     colapsavam no mesmo nome — o do responsável. Como a remarcação compara o
//     nome do aluno, marcar a aula do segundo ARQUIVAVA a do primeiro. O
//     remédio era pior que a doença que ele curava.
//
// Um nome de verdade não COMEÇA com "filho do", "não informado" ou "a
// confirmar"; uma descrição no lugar do nome, sim.
func NomeDoAluno(informado, responsavel string) string {
	n := strings.Join(strings.Fields(informado), " ")
	baixo := strings.ToLower(n)
	placeholder := n == ""
	for _, p := range []string{
		"não informado", "nao informado", "não informou", "nao informou",
		"a confirmar", "não sei", "nao sei", "desconhecid", "sem nome",
		"filho do", "filha do", "filho da", "filha da",
		"filho de", "filha de", "filho(", "filha(",
		"responsável", "responsavel",
	} {
		if strings.HasPrefix(baixo, p) {
			placeholder = true
			break
		}
	}
	if !placeholder {
		return n
	}
	if r := strings.Join(strings.Fields(responsavel), " "); r != "" {
		return r
	}
	return "a confirmar"
}
