package main

import (
	"math"
	"math/rand"
	"strings"
	"unicode"
)

// Casamento entre a resposta que o bot escreveu e o acervo de falas gravadas.
//
// POR QUE ASSIM. A voz do atendente não pode mais ser sintetizada — só existe o
// que já foi gravado. Então a pergunta não é "que áudio combina com o assunto?",
// e sim uma bem mais estreita: "existe uma gravação que diz EXATAMENTE isto que
// eu acabei de escrever?". Se existe, o cliente ouve o atendente. Se não existe,
// lê o texto — que já é a resposta certa — e a falta vira fila de gravação.
//
// A alternativa seria pedir ao modelo que escolhesse a gravação. Foi descartada:
// põe a decisão irreversível (qual frase gravada substitui a resposta e entra no
// histórico) num palpite, e o modelo não tem como saber que "R$ 1.970" não pode
// virar áudio nenhum porque nenhuma das 372 gravações fala número.
//
// O erro que este código comete é RECUSAR DEMAIS. É o erro certo: o custo de
// recusar é o cliente ler em vez de ouvir; o custo de errar o áudio é o
// atendente afirmar, com a própria voz, algo que ninguém disse.

// ── vocabulário ──────────────────────────────────────────────────────────────

// stopwordsPT: palavras sem conteúdo próprio. Saem da conta de cobertura para
// que "o", "de" e "que" não inflem a semelhança entre frases que não têm nada a
// ver uma com a outra.
//
// "não" NÃO está aqui de propósito — inverte o sentido da frase e é tratado
// como fato mais abaixo.
var stopwordsPT = map[string]bool{
	"a": true, "o": true, "as": true, "os": true, "um": true, "uma": true,
	"uns": true, "umas": true, "de": true, "da": true, "do": true, "das": true,
	"dos": true, "em": true, "na": true, "no": true, "nas": true, "nos": true,
	"por": true, "pelo": true, "pela": true, "para": true, "pra": true,
	"pro": true, "com": true, "sem": true, "que": true, "e": true, "ou": true,
	"se": true, "ao": true, "aos": true, "as_": true, "à": true, "às": true,
	"eu": true, "voce": true, "vc": true, "ele": true, "ela": true,
	"nos_": true, "eles": true, "elas": true, "me": true, "te": true,
	"lhe": true, "seu": true, "sua": true, "seus": true, "suas": true,
	"meu": true, "minha": true, "meus": true, "minhas": true,
	"este": true, "esta": true, "esse": true, "essa": true, "isso": true,
	"isto": true, "aquele": true, "aquela": true, "aquilo": true,
	"ja": true, "mas": true, "tambem": true, "muito": true, "bem": true,
	"aqui": true, "ai": true, "la": true, "so": true, "ser": true, "e_": true,
	"esta_": true, "estou": true, "sou": true, "ter": true, "tem": true,
	"tenho": true, "vai": true, "vou": true, "fica": true, "ficar": true,
	"the": true,
}

// fatosFixos: tokens que carregam DADO, não estilo. Se um lado da comparação
// afirma um destes e o outro não, não é a mesma fala — é outra informação.
//
// Qualquer token com dígito também é fato, por construção (preço, hora, data,
// CEP, telefone). A lista abaixo cobre o que aparece por extenso.
var fatosFixos = map[string]bool{
	// dias e períodos — "bom dia" não pode virar "boa noite"
	"segunda": true, "terca": true, "quarta": true, "quinta": true,
	"sexta": true, "sabado": true, "domingo": true, "feriado": true,
	"dia": true, "manha": true, "tarde": true, "noite": true, "madrugada": true,
	"hoje": true, "amanha": true, "ontem": true, "semana": true, "mes": true,

	// dinheiro e pagamento
	"reais": true, "real": true, "rs": true, "valor": true, "preco": true,
	"mensalidade": true, "matricula": true, "material": true,
	"pix": true, "boleto": true, "cartao": true, "dinheiro": true,
	"parcelado": true, "desconto": true, "gratis": true, "gratuita": true,
	"gratuito": true,

	// modalidade
	"online": true, "presencial": true, "individual": true, "turma": true,
	"particular": true,

	// nomes de curso e produto
	"excel": true, "word": true, "powerpoint": true, "office": true,
	"python": true, "robotica": true, "programacao": true, "informatica": true,
	"jogos": true, "roblox": true, "minecraft": true, "canva": true,
	"autocad": true, "blender": true, "unity": true, "sql": true,
	"git": true, "marketing": true, "design": true, "ia": true,
	"impressao": true, "3d": true,

	// negação — inverte tudo
	"nao": true,

	// NOME PRÓPRIO É DADO, não estilo.
	//
	// O acervo é a voz de uma pessoa, mas a persona do bot pode ter outro nome
	// — a escola usa um nome distinto justamente para reconhecer, quando o
	// cliente chega, que o atendimento foi do bot. Sem os nomes aqui, a
	// gravação "Aqui quem fala é o Henrique" casaria com "Aqui é o Marcos":
	// mesma frase, nome diferente, e o cliente ouviria o bot se apresentar
	// com o nome errado. O gate de fatos resolve sem regra nova.
	"henrique": true, "marcos": true, "bia": true, "julia": true,
	"rodrigo": true, "guilherme": true,
	// papel também é afirmação sobre quem está falando
	"coordenador": true, "coordenadora": true, "coordenacao": true,
	"professor": true, "professora": true,

	// "bom dia DE NOVO" afirma que já houve conversa antes
	"novo": true, "novamente": true, "denovo": true, "volta": true,
	"voltou": true, "retorno": true,

	// numerais por extenso: sem isto, "vinte minutinhos" casaria com
	// "quinze minutinhos"
	"zero": true, "dois": true, "duas": true, "tres": true, "quatro": true,
	"cinco": true, "seis": true, "sete": true, "oito": true, "nove": true,
	"dez": true, "onze": true, "doze": true, "treze": true, "quatorze": true,
	"catorze": true, "quinze": true, "dezesseis": true, "dezessete": true,
	"dezoito": true, "dezenove": true, "vinte": true, "trinta": true,
	"quarenta": true, "cinquenta": true, "sessenta": true, "setenta": true,
	"oitenta": true, "noventa": true, "cem": true, "cento": true,
	"duzentos": true, "trezentos": true, "quatrocentos": true,
	"quinhentos": true, "seiscentos": true, "setecentos": true,
	"oitocentos": true, "novecentos": true, "mil": true,
	"meia": true, "meio": true, "primeira": true, "primeiro": true,
}

// categoriasProativas: falas que a escola INICIA. Nunca são resposta a uma
// mensagem do cliente — mandar "só passando pra saber se está tudo certo no
// caminho" como resposta a uma dúvida é constrangedor, por mais que as palavras
// casem.
var categoriasProativas = map[string]bool{
	"no_show":       true,
	"lembrete_aula": true,
	"feriado":       true,
	"cobranca":      true,
	"tarefa":        true,
}

// ── normalização ─────────────────────────────────────────────────────────────

// normaliza tira acento, caixa e pontuação. Tabela explícita em vez de
// golang.org/x/text: são cinco vogais e um cedilha, não vale uma dependência.
var semAcento = strings.NewReplacer(
	"á", "a", "à", "a", "ã", "a", "â", "a", "ä", "a",
	"é", "e", "ê", "e", "è", "e", "ë", "e",
	"í", "i", "î", "i", "ì", "i", "ï", "i",
	"ó", "o", "ô", "o", "õ", "o", "ò", "o", "ö", "o",
	"ú", "u", "û", "u", "ù", "u", "ü", "u",
	"ç", "c", "ñ", "n",
)

func tokeniza(s string) []string {
	s = semAcento.Replace(strings.ToLower(s))
	campos := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(campos))
	for _, t := range campos {
		if t == "" || stopwordsPT[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

func temDigito(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// ehFato: o token carrega informação que não pode divergir entre a resposta e a
// gravação.
func ehFato(tok string) bool {
	return temDigito(tok) || fatosFixos[tok]
}

// separa devolve o conjunto de tokens de conteúdo e, dentro dele, os fatos.
func separa(texto string) (tokens, fatos map[string]bool) {
	tokens = map[string]bool{}
	fatos = map[string]bool{}
	for _, t := range tokeniza(texto) {
		tokens[t] = true
		if ehFato(t) {
			fatos[t] = true
		}
	}
	return tokens, fatos
}

func mesmosFatos(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// ── índice ───────────────────────────────────────────────────────────────────

type clipeIndexado struct {
	clip   AudioClip
	tokens map[string]bool
	fatos  map[string]bool
	peso   float64 // soma dos IDF dos tokens — o "tamanho informativo" da fala
}

// AudioIndex é o acervo preparado para busca. Montado uma vez por sincronização
// do manifesto e mantido em memória: são 372 falas, ~53 KB.
type AudioIndex struct {
	clipes []clipeIndexado
	idf    map[string]float64
	idfMax float64
}

// NovoAudioIndex calcula o IDF sobre o acervo e prepara cada fala para
// comparação. Falas sem transcrição ficam de fora: sem texto não há como saber
// o que elas dizem, e mandar áudio às cegas é exatamente o que não se quer.
func NovoAudioIndex(clips []AudioClip) *AudioIndex {
	ix := &AudioIndex{idf: map[string]float64{}}
	df := map[string]int{}

	for _, c := range clips {
		if strings.TrimSpace(c.Transcript) == "" {
			continue
		}
		toks, fatos := separa(c.Transcript)
		if len(toks) == 0 {
			continue
		}
		ix.clipes = append(ix.clipes, clipeIndexado{clip: c, tokens: toks, fatos: fatos})
		for t := range toks {
			df[t]++
		}
	}

	// IDF suavizado: o "+1" no fim é o que impede peso zero.
	//
	// Sem ele, um termo presente em TODAS as gravações recebe log(1)=0, e num
	// acervo pequeno — uma voz com meia dúzia de falas — isso zera o peso da
	// frase inteira e o casamento passa a recusar tudo, calado. O termo raro
	// continua pesando mais que o comum; ninguém pesa nada.
	n := float64(len(ix.clipes))
	for t, d := range df {
		ix.idf[t] = math.Log((n+1)/(float64(d)+1)) + 1
	}
	// Termo que não aparece em nenhuma gravação é o mais informativo que existe:
	// é conteúdo que o acervo inteiro não sabe dizer.
	ix.idfMax = math.Log(n+1) + 1

	for i := range ix.clipes {
		ix.clipes[i].peso = ix.pesoDe(ix.clipes[i].tokens)
	}
	return ix
}

func (ix *AudioIndex) pesoDe(toks map[string]bool) float64 {
	var soma float64
	for t := range toks {
		soma += ix.peso1(t)
	}
	return soma
}

func (ix *AudioIndex) peso1(t string) float64 {
	if v, ok := ix.idf[t]; ok {
		return v
	}
	return ix.idfMax
}

// ── casamento ────────────────────────────────────────────────────────────────

// MatchOpts — os botões do casamento, todos com default seguro.
type MatchOpts struct {
	MinScore     float64 // piso de semelhança (default 0.45)
	MaxDuracaoMs int     // teto duro de duração (default 12000)
	ConversaNova bool    // primeira mensagem: barra "bom dia DE NOVO"
}

// MatchInfo — por que deu no que deu. Vai para log e métrica: sem isto,
// "a voz parou de sair" é indistinguível de "ninguém mandou áudio hoje".
type MatchInfo struct {
	Motivo       string  // "casou" | "sem_candidato" | "resposta_vazia"
	Score        float64 // score do escolhido, ou do melhor quase-casamento
	NearMiss     string  // intenção que mais chegou perto quando não casou
	Candidatos   int     // quantos passaram em tudo
	Considerados int     // quantos chegaram a ser pontuados
}

// limiarPorDuracao: quanto mais longa a gravação, mais ela afirma — e mais
// parecida precisa ser da resposta para não afirmar nada a mais.
func limiarPorDuracao(base float64, ms int) float64 {
	switch {
	case ms <= 10000:
		return base
	case ms <= 20000:
		return base + 0.10
	default:
		return base + 0.20
	}
}

// Casa procura uma gravação que diga a mesma coisa que `resposta`.
//
// O funil, em ordem de custo crescente:
//  1. exclusões duras (categoria proativa, duração, conversa nova)
//  2. gate de fatos: os dados afirmados têm que ser IDÊNTICOS dos dois lados
//  3. cobertura bidirecional: score = min(quanto o clipe cobre da resposta,
//     quanto a resposta cobre do clipe). O mínimo é o coração disto — um lado
//     mede OMISSÃO, o outro mede INVENÇÃO, e só passa quem não faz nem uma nem
//     outra.
//  4. sorteio entre os aprovados, para a mesma fala não sair sempre igual.
func (ix *AudioIndex) Casa(resposta string, opts MatchOpts) (*AudioClip, MatchInfo) {
	if opts.MinScore <= 0 {
		opts.MinScore = 0.45
	}
	if opts.MaxDuracaoMs <= 0 {
		opts.MaxDuracaoMs = 12000
	}

	tokens, fatos := separa(resposta)
	if len(tokens) == 0 {
		return nil, MatchInfo{Motivo: "resposta_vazia"}
	}
	pesoResp := ix.pesoDe(tokens)
	if pesoResp == 0 {
		return nil, MatchInfo{Motivo: "resposta_vazia"}
	}

	// Resposta curtíssima ("Perfeito!") tem pouco a comparar, então um acerto
	// parcial não significa nada: exige casamento quase exato.
	minScore := opts.MinScore
	if len(tokens) < 2 {
		minScore = 0.90
	}

	type candidato struct {
		idx   int
		score float64
	}
	var aprovados []candidato
	info := MatchInfo{Motivo: "sem_candidato"}

	for i := range ix.clipes {
		ci := &ix.clipes[i]

		if categoriasProativas[ci.clip.Category] {
			continue
		}
		if ci.clip.DurationMs > opts.MaxDuracaoMs {
			continue
		}
		// "Bom dia de novo" para quem nunca falou com a gente.
		if opts.ConversaNova && strings.Contains(ci.clip.IntentKey, "retorno") {
			continue
		}
		if !mesmosFatos(fatos, ci.fatos) {
			continue
		}

		var inter float64
		for t := range ci.tokens {
			if tokens[t] {
				inter += ix.peso1(t)
			}
		}
		if inter == 0 || ci.peso == 0 {
			continue
		}
		covResp := inter / pesoResp // o clipe deixou de dizer algo?
		covClip := inter / ci.peso  // o clipe disse algo a mais?
		score := math.Min(covResp, covClip)

		info.Considerados++
		if score > info.Score {
			info.Score = score
			info.NearMiss = ci.clip.IntentKey
		}
		if score >= limiarPorDuracao(minScore, ci.clip.DurationMs) {
			aprovados = append(aprovados, candidato{idx: i, score: score})
		}
	}

	if len(aprovados) == 0 {
		return nil, info
	}
	info.Motivo = "casou"
	info.Candidatos = len(aprovados)

	// Sorteio com peso score³: tudo que chegou aqui é seguro, então o sorteio é
	// de graça e devolve a variedade que impede o cliente de perceber gravação.
	// O cubo mantém a preferência pelos melhores sem descartar os outros.
	var total float64
	for _, a := range aprovados {
		total += a.score * a.score * a.score
	}
	alvo := rand.Float64() * total
	escolhido := aprovados[len(aprovados)-1]
	for _, a := range aprovados {
		alvo -= a.score * a.score * a.score
		if alvo <= 0 {
			escolhido = a
			break
		}
	}
	info.Score = escolhido.score
	c := ix.clipes[escolhido.idx].clip
	return &c, info
}
