package main

import "testing"

func TestShouldReplyAsAudio(t *testing.T) {
	on := &VoiceClient{enabled: true}
	off := &VoiceClient{enabled: false}
	if !shouldReplyAsAudio(on, InboundMessage{WasVoice: true}) {
		t.Fatal("voz+on deveria ser true")
	}
	if shouldReplyAsAudio(on, InboundMessage{WasVoice: false}) {
		t.Fatal("texto não vira áudio")
	}
	if shouldReplyAsAudio(off, InboundMessage{WasVoice: true}) {
		t.Fatal("off deveria ser false")
	}
	if shouldReplyAsAudio(nil, InboundMessage{WasVoice: true}) {
		t.Fatal("nil deveria ser false")
	}
}
