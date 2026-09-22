package main

import (
	"testing"
	"time"
)

// O delay tem que ser proporcional ao texto QUE VEM — é o tempo de escrevê-lo.
func TestDelayCresceComOTamanhoDoTexto(t *testing.T) {
	curto := BetweenBubblesDelayMs("Tá certo!")
	medio := BetweenBubblesDelayMs("Com 9 anos ele entra no Tecnologia Júnior, que é o programa dessa faixa.")
	longo := BetweenBubblesDelayMs("Com 9 anos ele entra no Tecnologia Júnior: cria jogos e personagens no " +
		"Minecraft e no Roblox, e modela objetos em 3D que a gente imprime de verdade — ele leva pra casa. " +
		"A aula é individual, só ele e o professor, e o horário você escolhe.")

	if !(curto < medio && medio < longo) {
		t.Errorf("delays não crescem: curto=%v medio=%v longo=%v", curto, medio, longo)
	}
	// Ninguém espera mais que isso sem achar que a conversa travou.
	if longo > 6*time.Second {
		t.Errorf("texto longo pediu %v de espera — passa do teto", longo)
	}
	// E ninguém escreve uma frase em menos de um segundo.
	if curto < 900*time.Millisecond {
		t.Errorf("texto curto saiu em %v — rápido demais para parecer digitado", curto)
	}
}

// Depois do áudio a pausa é maior: quem acabou de gravar não digita no mesmo
// segundo. Sem isso os dois chegam juntos e o conjunto denuncia automação.
func TestPausaDepoisDoAudioEMaiorQueEntreTextos(t *testing.T) {
	texto := "Que dia da semana fica melhor pra vocês?"
	audio := 4 * time.Second

	depoisDoAudio := DepoisDoAudioDelayMs(audio, texto)
	entreTextos := BetweenBubblesDelayMs(texto)

	if depoisDoAudio <= entreTextos {
		t.Errorf("depois do áudio (%v) deveria ser maior que entre textos (%v)", depoisDoAudio, entreTextos)
	}
	// Áudio mais longo, pausa maior — a pessoa ouve o próprio áudio sair.
	if DepoisDoAudioDelayMs(10*time.Second, texto) <= DepoisDoAudioDelayMs(2*time.Second, texto) {
		t.Error("a duração do áudio deveria influenciar a pausa")
	}
	if got := DepoisDoAudioDelayMs(60*time.Second, texto); got > 9*time.Second {
		t.Errorf("pausa de %v depois de um áudio longo — passa do teto", got)
	}
}
