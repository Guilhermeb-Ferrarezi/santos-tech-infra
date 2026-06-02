package main

import "github.com/alexedwards/argon2id"

// hashPassword gera um hash argon2id no formato PHC (compatível com Bun.password).
func hashPassword(p string) (string, error) {
	return argon2id.CreateHash(p, argon2id.DefaultParams)
}

// verifyPassword confere a senha. Lê os parâmetros do próprio hash PHC,
// então verifica hashes gerados pelo Bun.password sem problema.
func verifyPassword(p, hash string) bool {
	match, err := argon2id.ComparePasswordAndHash(p, hash)
	return err == nil && match
}
