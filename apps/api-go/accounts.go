package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// Cookie "accounts": índice ASSINADO (HMAC-SHA256 com o JWT_SECRET) das sessões
// conhecidas neste navegador, pro multi-conta (chooser estilo Google). Não dá
// acesso por si só — uso/ativação sempre valida a sessão real no Postgres.
const (
	accountsCookieName = "accounts"
	maxAccounts        = 5
)

var sessionIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// signAccountsValue serializa ids como "id1|id2.<hmac-hex>".
func signAccountsValue(secret string, ids []string) string {
	payload := strings.Join(ids, "|")
	return payload + "." + accountsMAC(secret, payload)
}

// parseAccountsValue valida a assinatura e o formato dos ids; nil se inválido.
func parseAccountsValue(secret, value string) []string {
	i := strings.LastIndex(value, ".")
	if i < 0 {
		return nil
	}
	payload, sig := value[:i], value[i+1:]
	if !hmac.Equal([]byte(sig), []byte(accountsMAC(secret, payload))) {
		return nil
	}
	if payload == "" {
		return nil
	}
	ids := strings.Split(payload, "|")
	for _, id := range ids {
		if !sessionIDRe.MatchString(id) {
			return nil
		}
	}
	return ids
}

func accountsMAC(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// readAccounts lê os session ids do cookie (nil se ausente/inválido).
func (s *Server) readAccounts(r *http.Request) []string {
	c, err := r.Cookie(accountsCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	return parseAccountsValue(s.cfg.JWTSecret, c.Value)
}

// writeAccounts regrava o cookie (lista vazia → expira o cookie).
func (s *Server) writeAccounts(w http.ResponseWriter, ids []string) {
	if len(ids) == 0 {
		s.setCookie(w, accountsCookieName, "", -1)
		return
	}
	s.setCookie(w, accountsCookieName, signAccountsValue(s.cfg.JWTSecret, ids), int(refreshTTL.Seconds()))
}

// appendAccount adiciona sid ao fim da lista (removendo `remove` e duplicatas),
// com teto de maxAccounts — estoura pelo mais antigo. Devolve a lista gravada.
func (s *Server) appendAccount(w http.ResponseWriter, r *http.Request, sid string, remove ...string) []string {
	ids := s.readAccounts(r)
	out := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		if id != sid && !slices.Contains(remove, id) {
			out = append(out, id)
		}
	}
	out = append(out, sid)
	if len(out) > maxAccounts {
		out = out[len(out)-maxAccounts:]
	}
	s.writeAccounts(w, out)
	return out
}

// removeAccount tira sid da lista e regrava o cookie. Devolve a lista gravada.
func (s *Server) removeAccount(w http.ResponseWriter, r *http.Request, sid string) []string {
	ids := s.readAccounts(r)
	out := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == sid })
	s.writeAccounts(w, out)
	return out
}
