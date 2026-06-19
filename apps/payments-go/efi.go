package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// loadClientCert decodifica o .p12 (base64) e monta um tls.Certificate pronto para
// o mTLS da Efí. O .p12 da Efí costuma vir com senha vazia.
func loadClientCert(p12Base64, password string) (tls.Certificate, error) {
	raw, err := base64.StdEncoding.DecodeString(p12Base64)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("efi: base64 do certificado inválido: %w", err)
	}
	key, leaf, err := pkcs12.Decode(raw, password)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("efi: falha ao parsear .p12: %w", err)
	}
	cert := tls.Certificate{
		Certificate: [][]byte{leaf.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}
	// Garante que a chave casa com o leaf (e popula campos internos do tls).
	if _, err := x509.ParseCertificate(leaf.Raw); err != nil {
		return tls.Certificate{}, fmt.Errorf("efi: certificado inválido: %w", err)
	}
	return cert, nil
}
