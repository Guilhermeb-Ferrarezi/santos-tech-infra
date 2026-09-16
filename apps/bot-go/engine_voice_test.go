package main

import "testing"

func TestShouldReplyAsAudio(t *testing.T) {
	on := &VoiceClient{enabled: true}
	off := &VoiceClient{enabled: false}
	cfgOn := TenantConfig{VoiceEnabled: true}
	cfgOff := TenantConfig{VoiceEnabled: false}

	if !shouldReplyAsAudio(on, cfgOn, InboundMessage{WasVoice: true}) {
		t.Fatal("voz+on deveria ser true")
	}
	if shouldReplyAsAudio(on, cfgOn, InboundMessage{WasVoice: false}) {
		t.Fatal("texto não vira áudio")
	}
	if shouldReplyAsAudio(off, cfgOn, InboundMessage{WasVoice: true}) {
		t.Fatal("off deveria ser false")
	}
	if shouldReplyAsAudio(nil, cfgOn, InboundMessage{WasVoice: true}) {
		t.Fatal("nil deveria ser false")
	}
	// O painel pode desligar a voz de um tenant mesmo com o ambiente ligado.
	if shouldReplyAsAudio(on, cfgOff, InboundMessage{WasVoice: true}) {
		t.Fatal("tenant com voz desligada deveria ser false")
	}
}
