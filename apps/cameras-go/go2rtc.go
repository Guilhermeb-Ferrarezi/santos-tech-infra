package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
)

type Go2RTCClient struct {
	apiURL string
	client *http.Client
}

func NewGo2RTCClient() *Go2RTCClient {
	apiURL := os.Getenv("GO2RTC_URL") // ex: http://localhost:1984
	if apiURL == "" {
		apiURL = "http://localhost:1984"
	}
	return &Go2RTCClient{
		apiURL: apiURL,
		client: &http.Client{},
	}
}

// RegisterStream adiciona a câmera no go2rtc
func (g *Go2RTCClient) RegisterStream(name, src string) error {
	u := fmt.Sprintf("%s/api/streams?name=%s&src=%s", g.apiURL, url.QueryEscape(name), url.QueryEscape(src))
	req, _ := http.NewRequest("PUT", u, nil) // go2rtc usa PUT ou POST para streams, via querystring
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("erro no go2rtc: %d - %s", resp.StatusCode, string(b))
	}
	return nil
}

// DeleteStream remove a câmera do go2rtc
func (g *Go2RTCClient) DeleteStream(name string) error {
	u := fmt.Sprintf("%s/api/streams?name=%s", g.apiURL, url.QueryEscape(name))
	req, _ := http.NewRequest("DELETE", u, nil)
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// ProxyStream retorna um handler de proxy reverso para o MSE WebSocket do go2rtc
func (g *Go2RTCClient) ProxyStream(cameraName string) http.HandlerFunc {
	target, _ := url.Parse(g.apiURL)
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Modificar o request para apontar pro stream certo do MSE
	// O endpoint MSE do go2rtc é /api/ws?src={name}
	return func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/api/ws"
		q := r.URL.Query()
		q.Set("src", cameraName)
		r.URL.RawQuery = q.Encode()
		proxy.ServeHTTP(w, r)
	}
}
