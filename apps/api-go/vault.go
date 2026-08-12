package main

// Vault criptografa/descriptografa segredos (chaves de API de terceiros do
// roteador de failover) antes de persistir no Postgres. AES-256-GCM com a
// chave derivada via SHA-256 do secret (permite qualquer tamanho de
// passphrase em API_VAULT_SECRET). Vazio (Vault nil) = feature desabilitada,
// os handlers respondem 503 (ver handlers_apirouter.go).

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

type Vault struct {
	key [32]byte
}

// newVault deriva a chave AES-256 a partir do secret. secret vazio → nil
// (desabilitado); o caller deve checar antes de usar.
func newVault(secret string) *Vault {
	if secret == "" {
		return nil
	}
	return &Vault{key: sha256.Sum256([]byte(secret))}
}

// Encrypt cifra o texto puro e devolve base64(nonce || ciphertext).
func (v *Vault) Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(v.key[:])
	if err != nil {
		return "", fmt.Errorf("vault: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("vault: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("vault: nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverte Encrypt. Erro se a chave estiver errada ou o valor corrompido.
func (v *Vault) Decrypt(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("vault: base64: %w", err)
	}
	block, err := aes.NewCipher(v.key[:])
	if err != nil {
		return "", fmt.Errorf("vault: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("vault: gcm: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("vault: ciphertext curto demais")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("vault: decrypt: %w", err)
	}
	return string(plaintext), nil
}

// maskSecret devolve só os últimos 4 caracteres do segredo, para exibir no
// frontend sem nunca reenviar o valor completo pro cliente.
func maskSecret(secret string) string {
	const visible = 4
	if len(secret) <= visible {
		return ""
	}
	return secret[len(secret)-visible:]
}
