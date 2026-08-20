package main

// Vault criptografa/descriptografa segredos (chaves de API de terceiros do
// roteador de failover) antes de persistir no Postgres. AES-256-GCM.
// Vazio (Vault nil) = feature desabilitada, os handlers respondem 503 (ver
// handlers_apirouter.go).
//
// ── Dois formatos no banco ───────────────────────────────────────────────────
//
//	v1 (legado): base64(nonce || ciphertext), chave = sha256(API_VAULT_SECRET),
//	             AAD nil. Sem KDF e sem salt, e como o AAD era nil um blob podia
//	             ser copiado de uma linha para outra (de outro provider,
//	             inclusive) e continuava decifrando — bastava escrita no banco
//	             para redirecionar uma chave.
//
//	v2 (atual):  "v2." + base64(nonce || ciphertext), chave derivada por
//	             HKDF-SHA256 sobre API_VAULT_SECRET com salt de config
//	             (API_VAULT_SALT), e AAD amarrando o blob à linha
//	             (provider + tail do segredo).
//
// A LEITURA aceita os dois: tenta v2 e cai no v1. É isso que impede o cofre de
// produção de virar lixo no deploy — todas as chaves já cadastradas estão em
// v1. A ESCRITA é sempre v2.
//
// O prefixo "v2." é inambíguo porque "." não pertence ao alfabeto base64
// padrão: um blob v1 nunca começa com ele.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	vaultV2Prefix = "v2."

	// vaultHKDFInfo separa o domínio de uso da chave derivada (RFC 5869 §3.2).
	vaultHKDFInfo = "santos-tech/api-go/api-router-key/v2"

	// vaultDefaultSalt é usado quando API_VAULT_SALT não está setada. ATENÇÃO:
	// mudar o salt (ou sair do default para um valor próprio) torna ilegível
	// tudo que já foi cifrado em v2 — as chaves precisariam ser recadastradas.
	vaultDefaultSalt = "santos-tech/api-vault/default-salt"
)

type Vault struct {
	// key: HKDF-SHA256(secret, salt, info). Cifra tudo daqui pra frente e
	// decifra os blobs v2.
	key [32]byte
	// legacyKey: sha256(secret), a derivação antiga. Existe SÓ para ler o que
	// já está no banco em v1 — nada novo é escrito com ela.
	legacyKey [32]byte
}

// newVault deriva as chaves AES-256 a partir do secret. secret vazio → nil
// (desabilitado); o caller deve checar antes de usar. salt vazio cai no
// vaultDefaultSalt.
func newVault(secret, salt string) *Vault {
	if secret == "" {
		return nil
	}
	if salt == "" {
		salt = vaultDefaultSalt
	}
	v := &Vault{legacyKey: sha256.Sum256([]byte(secret))}
	derived, err := hkdf.Key(sha256.New, []byte(secret), []byte(salt), vaultHKDFInfo, 32)
	if err != nil {
		// hkdf.Key só falha com parâmetros impossíveis (keyLength absurdo);
		// com sha256 e 32 bytes não acontece. Ainda assim, não seguimos com
		// uma chave zerada: sem cofre é melhor que cofre falso.
		return nil
	}
	copy(v.key[:], derived)
	return v
}

// apiRouterKeyAAD amarra o blob à linha da chave: provider (para o segredo não
// poder ser movido para outro provider, que aponta para outro host) e tail do
// segredo (para não poder ser movido para outra linha do mesmo provider).
//
// O ID da chave ficaria de fora de propósito: ele só existe DEPOIS do INSERT, e
// a cifragem acontece antes. O tail é o substituto imutável — nunca é alterado
// depois de gravado (UpdateAPIRouterKeyMeta só mexe em label/priority).
func apiRouterKeyAAD(providerID int64, secretTail string) []byte {
	return []byte("apirouter|provider=" + strconv.FormatInt(providerID, 10) + "|tail=" + secretTail)
}

// EncryptKeySecret cifra o segredo de UMA chave do roteador no formato v2,
// amarrado ao provider e ao tail. Sempre grava v2.
func (v *Vault) EncryptKeySecret(plaintext string, providerID int64, secretTail string) (string, error) {
	sealed, err := seal(v.key, plaintext, apiRouterKeyAAD(providerID, secretTail))
	if err != nil {
		return "", err
	}
	return vaultV2Prefix + sealed, nil
}

// DecryptKeySecret decifra o segredo de uma chave aceitando os DOIS formatos.
// legacy=true significa que o valor ainda está em v1 e vale a pena reescrever
// (ver decryptAPIRouterKeySecret em handlers_apirouter.go).
func (v *Vault) DecryptKeySecret(encoded string, providerID int64, secretTail string) (plaintext string, legacy bool, err error) {
	if after, ok := strings.CutPrefix(encoded, vaultV2Prefix); ok {
		p, err := open(v.key, after, apiRouterKeyAAD(providerID, secretTail))
		return p, false, err
	}
	p, err := open(v.legacyKey, encoded, nil)
	return p, err == nil, err
}

// seal cifra e devolve base64(nonce || ciphertext).
func seal(key [32]byte, plaintext string, aad []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("vault: nonce: %w", err)
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plaintext), aad)), nil
}

// open reverte seal. Erro se a chave estiver errada, o AAD não bater ou o valor
// estiver corrompido.
func open(key [32]byte, encoded string, aad []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("vault: base64: %w", err)
	}
	gcm, err := newGCM(key)
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

// maskSecret devolve só os últimos 4 caracteres do segredo, para exibir no
// frontend sem nunca reenviar o valor completo pro cliente.
func maskSecret(secret string) string {
	const visible = 4
	if len(secret) <= visible {
		return ""
	}
	return secret[len(secret)-visible:]
}
