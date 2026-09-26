package main

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{
		"code":    code,
		"message": message,
	})
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	if cookie, err := r.Cookie("access_token"); err == nil {
		return cookie.Value
	}
	if cookie, err := r.Cookie("st_token"); err == nil {
		return cookie.Value
	}
	if cookie, err := r.Cookie("santos_token"); err == nil {
		return cookie.Value
	}
	return r.URL.Query().Get("token")
}
