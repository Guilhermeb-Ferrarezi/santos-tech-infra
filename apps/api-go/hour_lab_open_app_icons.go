package main

// Ícone de cada aplicativo aberto num PC do laboratório, pra listagem admin.
//
// A imagem NÃO chega por um caminho novo: o app (hour-timer-app) já sobe, no
// inventário, o ícone de cada programa instalado, extraído do próprio
// executável (ver hour_lab_inventory.go / list_installed_programs). Aqui só se
// cruza o NOME do app aberto (openApps, que o app manda desde a 0.1.9) com esse
// inventário — nada muda no app, e o dashboard ganha o ícone real do Windows
// pra qualquer PC que já reporta programas.
//
// É heurística de nome: "Unity" casa com "Unity 6000.0.23f1", "Visual Studio
// Code" com "Microsoft Visual Studio Code (User)". Quando não casa, o nome fica
// fora do mapa e o dashboard mostra um placeholder — errar o ícone seria pior
// que não ter.

import (
	"context"
	"strings"
	"unicode"
)

// labProgramIconRef é o mínimo do inventário que o cruzamento por nome usa.
type labProgramIconRef struct {
	Name string
	Hash string
}

// resolveOpenAppIcons preenche OpenAppIcons de cada PC cruzando os nomes de app
// aberto com o inventário de programas do próprio PC. Uma query só pra todos os
// PCs da página — a listagem é chamada a cada 10s por cada admin com o
// dashboard aberto, então não pode virar N+1.
func (s *Server) resolveOpenAppIcons(ctx context.Context, devices []LabDevice) error {
	var need []string
	for i := range devices {
		d := &devices[i]
		if d.OpenAppIcons == nil {
			d.OpenAppIcons = map[string]string{}
		}
		if len(d.OpenApps) > 0 {
			need = append(need, d.ID)
		}
	}
	if len(need) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT device_id::text, name, icon_hash FROM hour_lab_device_programs
		WHERE device_id = ANY($1::uuid[]) AND icon_hash IS NOT NULL
		ORDER BY device_id, name`, need)
	if err != nil {
		return err
	}
	defer rows.Close()
	byDevice := make(map[string][]labProgramIconRef, len(need))
	for rows.Next() {
		var id string
		var p labProgramIconRef
		if err := rows.Scan(&id, &p.Name, &p.Hash); err != nil {
			return err
		}
		byDevice[id] = append(byDevice[id], p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range devices {
		d := &devices[i]
		programs := byDevice[d.ID]
		if len(programs) == 0 {
			continue
		}
		for _, app := range d.OpenApps {
			if h := matchOpenAppIcon(app, programs); h != "" {
				d.OpenAppIcons[app] = h
			}
		}
	}
	return nil
}

// appNameStopTokens são palavras que aparecem no nome de instalação e não no
// nome da janela (ou vice-versa) sem dizer nada sobre QUAL programa é.
var appNameStopTokens = map[string]bool{"user": true, "bit": true, "edition": true}

// appNameTokens reduz um nome a palavras comparáveis: minúsculas, só letras e
// dígitos como separador, sem tokens de versão/arquitetura ("6000.0.23f1",
// "x64", "24.08") nem as palavras de ruído acima. É o que faz "Unity" e
// "Unity 6000.0.23f1" virarem a mesma coisa.
func appNameTokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if appNameStopTokens[f] || strings.ContainsFunc(f, unicode.IsDigit) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// matchOpenAppIcon escolhe, entre os programas instalados no PC, o ícone que
// melhor corresponde ao nome de um app aberto. Da preferência mais forte pra
// mais fraca:
//
//   - mesmas palavras, na mesma ordem ("Discord" ↔ "Discord", "Unity" ↔
//     "Unity 6000.0.23f1");
//   - todas as palavras do app estão no programa ("Visual Studio Code" ⊂
//     "Microsoft Visual Studio Code (User)") — quanto menos sobra, melhor;
//   - todas as palavras do programa estão no app ("Google Chrome" ⊂ "Google
//     Chrome Beta"), só com programa de 2+ palavras: uma palavra solta
//     ("Microsoft") casaria com metade do inventário.
//
// Empate desfaz por nome, pra o resultado não dançar entre requisições.
// Devolve "" quando nada casa — placeholder é melhor que ícone errado.
func matchOpenAppIcon(app string, programs []labProgramIconRef) string {
	appTokens := appNameTokens(app)
	if len(appTokens) == 0 {
		return ""
	}
	// Nome curtíssimo ("VS", "OS") só casa por igualdade exata.
	short := len(appTokens) == 1 && len(appTokens[0]) < 3
	appSet := tokenSet(appTokens)

	bestHash, bestName := "", ""
	bestScore := 0
	for _, p := range programs {
		if p.Hash == "" {
			continue
		}
		pt := appNameTokens(p.Name)
		if len(pt) == 0 {
			continue
		}
		var score int
		switch {
		case equalTokens(appTokens, pt):
			score = 3000
		case short:
			continue
		case isSubset(appSet, tokenSet(pt)):
			score = 2000 - len(pt)
		case len(pt) >= 2 && isSubset(tokenSet(pt), appSet):
			score = 1000 + len(pt)
		default:
			continue
		}
		lower := strings.ToLower(p.Name)
		if score > bestScore || (score == bestScore && lower < bestName) {
			bestScore, bestHash, bestName = score, p.Hash, lower
		}
	}
	return bestHash
}

func tokenSet(tokens []string) map[string]bool {
	set := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		set[t] = true
	}
	return set
}

func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isSubset(sub, super map[string]bool) bool {
	for t := range sub {
		if !super[t] {
			return false
		}
	}
	return true
}
