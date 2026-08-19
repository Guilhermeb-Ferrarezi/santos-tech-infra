package main

// Vault cifra o valor bruto da chave vazada (matchedValue) antes de ele sair
// deste processo. AES-256-GCM, mesmo padrão do vault.go do api-go (envelope
// simples: "v1." + base64(nonce || ciphertext), chave derivada por
// HKDF-SHA256).
//
// Por que existe: o Elasticsearch guarda chaves reais de terceiros (OpenAI,
// Stripe, GitHub, Mercado Pago...). O cliente ES aqui não tem credencial
// própria e o deploy padrão aponta para um cluster sem TLS — qualquer um com
// acesso de rede ao 9200 lia o índice inteiro em claro. Cifrando em repouso,
// quem lê o índice fica com blob inútil sem a chave de ambiente.
//
// O AAD amarra o blob ao documento (id determinístico repo|path|keyword), então
// um ciphertext não pode ser copiado de um hit para outro dentro do índice.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	vaultPrefix = "v1."

	// vaultHKDFInfo separa o domínio de uso da chave derivada (RFC 5869 §3.2).
	vaultHKDFInfo = "santos-tech/secrets-go/matched-value/v1"

	// vaultFallbackInfo é usado quando não há SECRETS_VAULT_SECRET e a chave é
	// derivada do JWT_SECRET. Info diferente = chave diferente, então trocar de
	// um para o outro não decifra o que já foi gravado.
	vaultFallbackInfo = "santos-tech/secrets-go/matched-value/v1-from-jwt"

	// vaultDefaultSalt vale quando SECRETS_VAULT_SALT não está setada. ATENÇÃO:
	// mudar o salt (ou o secret) torna ilegível tudo que já foi cifrado — os
	// valores precisariam ser re-escaneados.
	vaultDefaultSalt = "santos-tech/secrets-go/default-salt"
)

var errVaultDisabled = errors.New("vault: não configurado")

type Vault struct {
	key [32]byte
}

// newVault deriva a chave AES-256. secret vazio → nil (o caller decide o que
// fazer; ver LoadConfig/NewElasticClient).
func newVault(secret, salt, info string) *Vault {
	if secret == "" {
		return nil
	}
	if salt == "" {
		salt = vaultDefaultSalt
	}
	derived, err := hkdf.Key(sha256.New, []byte(secret), []byte(salt), info, 32)
	if err != nil {
		// Só falha com parâmetros impossíveis; ainda assim, sem cofre é melhor
		// que cofre falso (chave zerada).
		return nil
	}
	v := &Vault{}
	copy(v.key[:], derived)
	return v
}

// hitAAD amarra o blob ao documento: o mesmo ciphertext não decifra em outro id.
func hitAAD(docID string) []byte { return []byte("dotfy-hit|" + docID) }

// Encrypt devolve "v1." + base64(nonce || ciphertext).
func (v *Vault) Encrypt(plaintext string, aad []byte) (string, error) {
	if v == nil {
		return "", errVaultDisabled
	}
	gcm, err := newGCM(v.key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("vault: nonce: %w", err)
	}
	return vaultPrefix + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), aad)), nil
}

// Decrypt reverte Encrypt. Erro se a chave estiver errada, o AAD não bater ou o
// valor estiver corrompido.
func (v *Vault) Decrypt(encoded string, aad []byte) (string, error) {
	if v == nil {
		return "", errVaultDisabled
	}
	after, ok := strings.CutPrefix(encoded, vaultPrefix)
	if !ok {
		return "", errors.New("vault: formato desconhecido")
	}
	raw, err := base64.StdEncoding.DecodeString(after)
	if err != nil {
		return "", fmt.Errorf("vault: base64: %w", err)
	}
	gcm, err := newGCM(v.key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("vault: ciphertext curto demais")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", fmt.Errorf("vault: decrypt: %w", err)
	}
	return string(plaintext), nil
}

func newGCM(key [32]byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("vault: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vault: gcm: %w", err)
	}
	return gcm, nil
}

// ── Fingerprint exibível ─────────────────────────────────────────────────
// A listagem devolve prefixo + hash, nunca o valor. O prefixo é o que
// identifica o provedor na UI (sk-ant-, ghp_, xoxb-...) e o hash serve para
// correlacionar/deduplicar dois achados da mesma chave sem revelá-la.

// valuePrefix devolve os primeiros caracteres do valor, no máximo um terço
// dele — chave curta nunca vira "quase o valor inteiro".
func valuePrefix(value string) string {
	n := 8
	if third := len(value) / 3; third < n {
		n = third
	}
	if n <= 0 {
		return ""
	}
	return strings.ToValidUTF8(value[:n], "") + "…"
}

// valueHash é o sha256 hex do valor — fingerprint estável para deduplicação e
// correlação. Chaves de API têm entropia alta o bastante para o hash não ser
// reversível na prática.
func valueHash(value string) string {
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
