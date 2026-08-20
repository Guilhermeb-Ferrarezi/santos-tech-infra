package main

import (
	"crypto/sha256"
	"strings"
	"testing"
)

const (
	tVaultSecret = "segredo-de-teste-123"
	tVaultSalt   = "salt-de-teste"
)

func TestVaultEncryptDecryptRoundTrip(t *testing.T) {
	v := newVault(tVaultSecret, tVaultSalt)
	plaintext := "sk-live-abcdef1234567890"

	enc, err := v.EncryptKeySecret(plaintext, 7, maskSecret(plaintext))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == plaintext {
		t.Fatal("valor cifrado igual ao plaintext")
	}
	if !strings.HasPrefix(enc, vaultV2Prefix) {
		t.Fatalf("escrita nova tem que ser v2, veio %q", enc[:min(8, len(enc))])
	}

	dec, legacy, err := v.DecryptKeySecret(enc, 7, maskSecret(plaintext))
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if legacy {
		t.Error("blob v2 não pode ser reportado como legado")
	}
	if dec != plaintext {
		t.Fatalf("dec=%q, queria %q", dec, plaintext)
	}
}

// A garantia que não pode falhar em produção: TODO segredo já gravado está no
// formato v1 (base64 puro, chave sha256, sem AAD). Se a leitura não aceitar
// esse formato, o cofre vira lixo no deploy.
func TestVaultLeFormatoLegado(t *testing.T) {
	plaintext := "sk-live-antigo-0000"
	// Reproduz exatamente o que a versão anterior gravava.
	legacyKey := sha256.Sum256([]byte(tVaultSecret))
	blobV1, err := seal(legacyKey, plaintext, nil)
	if err != nil {
		t.Fatalf("seal legado: %v", err)
	}
	if strings.HasPrefix(blobV1, vaultV2Prefix) {
		t.Fatal("blob v1 nunca pode começar com o prefixo v2")
	}

	v := newVault(tVaultSecret, tVaultSalt)
	dec, legacy, err := v.DecryptKeySecret(blobV1, 7, maskSecret(plaintext))
	if err != nil {
		t.Fatalf("decrypt do formato antigo: %v", err)
	}
	if dec != plaintext {
		t.Fatalf("dec=%q, queria %q", dec, plaintext)
	}
	if !legacy {
		t.Error("blob v1 tem que ser reportado como legado (para reescrita)")
	}
	// O v1 não tinha AAD: provider/tail diferentes não podem impedir a leitura,
	// senão chaves já gravadas ficariam inacessíveis.
	if _, _, err := v.DecryptKeySecret(blobV1, 999, "zzzz"); err != nil {
		t.Errorf("v1 não deveria depender de provider/tail: %v", err)
	}
}

// AAD: o blob v2 fica amarrado à linha. Copiá-lo para outro provider (que
// aponta para outro host) ou para outra chave não decifra mais.
func TestVaultV2NaoMigraDeLinha(t *testing.T) {
	v := newVault(tVaultSecret, tVaultSalt)
	plaintext := "sk-live-abcdef1234567890"
	tail := maskSecret(plaintext)

	enc, err := v.EncryptKeySecret(plaintext, 7, tail)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, _, err := v.DecryptKeySecret(enc, 8, tail); err == nil {
		t.Error("blob movido para outro provider não deveria decifrar")
	}
	if _, _, err := v.DecryptKeySecret(enc, 7, "9999"); err == nil {
		t.Error("blob movido para outra chave não deveria decifrar")
	}
	if _, _, err := v.DecryptKeySecret(enc, 7, tail); err != nil {
		t.Errorf("na própria linha tem que decifrar: %v", err)
	}
}

func TestVaultDecryptComChaveErradaFalha(t *testing.T) {
	v1 := newVault("chave-um", tVaultSalt)
	v2 := newVault("chave-dois", tVaultSalt)

	enc, err := v1.EncryptKeySecret("segredo", 1, "redo")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, _, err := v2.DecryptKeySecret(enc, 1, "redo"); err == nil {
		t.Fatal("esperava erro ao decriptar com chave errada")
	}
}

// HKDF com salt diferente = chave diferente. Documenta o footgun: trocar
// API_VAULT_SALT torna ilegível o que já foi cifrado em v2.
func TestVaultSaltDiferenteMudaAChave(t *testing.T) {
	a := newVault(tVaultSecret, "salt-a")
	b := newVault(tVaultSecret, "salt-b")
	if a.key == b.key {
		t.Fatal("salts diferentes deveriam derivar chaves diferentes")
	}
	// O legado independe do salt (era sha256 puro do secret) — é o que garante
	// que blobs v1 continuam legíveis mesmo se o salt mudar.
	if a.legacyKey != b.legacyKey {
		t.Fatal("a chave legada não pode depender do salt")
	}

	// Salt vazio é determinístico (cai no default), não aleatório.
	if newVault(tVaultSecret, "").key != newVault(tVaultSecret, vaultDefaultSalt).key {
		t.Fatal("salt vazio tem que cair no vaultDefaultSalt")
	}
}

// A derivação nova não pode coincidir com a antiga — senão o HKDF não estaria
// fazendo nada.
func TestVaultHKDFNaoEhSha256Puro(t *testing.T) {
	v := newVault(tVaultSecret, tVaultSalt)
	if v.key == v.legacyKey {
		t.Fatal("a chave v2 não pode ser igual ao sha256 puro do secret")
	}
}

func TestNewVaultSecretVazioDevolveNil(t *testing.T) {
	if v := newVault("", tVaultSalt); v != nil {
		t.Fatalf("newVault(\"\") = %v, queria nil (feature desabilitada)", v)
	}
}

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"sk-live-abcdef1234567890": "7890",
		"ab":                       "",
		"":                         "",
	}
	for in, want := range cases {
		if got := maskSecret(in); got != want {
			t.Errorf("maskSecret(%q) = %q, queria %q", in, got, want)
		}
	}
}
