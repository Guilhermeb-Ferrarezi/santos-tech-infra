package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const devID = "2a502d12-676b-4723-80ae-b65335514df9"

func TestDeviceCommandRunEnfileiraEEsperaResultado(t *testing.T) {
	var polls atomic.Int32
	var gotBody, gotAuth string
	api := httptest.NewServer(authMeOK(4, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/hour-lab-devices/"+devID+"/command":
			b, _ := io.ReadAll(r.Body)
			gotBody, gotAuth = string(b), r.Header.Get("Authorization")
			w.WriteHeader(201)
			w.Write([]byte(`{"commandId":"c0ffee00-0000-0000-0000-000000000001"}`))
		case r.Method == "GET" && r.URL.Path == "/hour-lab-devices/"+devID+"/commands/c0ffee00-0000-0000-0000-000000000001":
			if polls.Add(1) < 2 {
				w.Write([]byte(`{"command":{"id":"c0ffee00-0000-0000-0000-000000000001","result":null}}`))
				return
			}
			w.Write([]byte(`{"command":{"id":"c0ffee00-0000-0000-0000-000000000001","result":"GAZAKE\r\n"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	prev := devicePollInterval
	devicePollInterval = 10 * time.Millisecond
	t.Cleanup(func() { devicePollInterval = prev })

	session := newTestSession(t, Config{AuthBaseURL: api.URL}, nil, "Bearer st_biel")
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "device_command_run", Arguments: map[string]any{"id": devID, "powershell": "hostname"},
	})
	if err != nil {
		t.Fatal(err)
	}
	txt := toolText(t, res)
	if res.IsError || !strings.Contains(txt, "GAZAKE") {
		t.Fatalf("esperava o resultado: %s", txt)
	}
	if gotAuth != "Bearer st_biel" || !strings.Contains(gotBody, `"text":"hostname"`) {
		t.Fatalf("auth=%q body=%q", gotAuth, gotBody)
	}
}

func TestDeviceCommandRunSemPermissaoViraErro(t *testing.T) {
	api := httptest.NewServer(authMeOK(4, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte(`{"code":"FORBIDDEN","message":"Acesso restrito"}`))
	}))
	defer api.Close()
	session := newTestSession(t, Config{AuthBaseURL: api.URL}, nil, "Bearer st_x")
	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "device_command_run", Arguments: map[string]any{"id": devID, "powershell": "hostname"},
	})
	if !res.IsError || !strings.Contains(toolText(t, res), "403") {
		t.Fatalf("esperava erro 403: %s", toolText(t, res))
	}
}

func TestDeviceControlValidaAcao(t *testing.T) {
	session := newTestSession(t, Config{AuthBaseURL: newAuthMeServer(t, 3).URL}, nil, "Bearer st_x")
	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "device_control", Arguments: map[string]any{"id": devID, "action": "formatar"},
	})
	if !res.IsError {
		t.Fatal("ação inválida deveria falhar localmente")
	}
}

func TestDevicesListRepassaToken(t *testing.T) {
	var gotURL string
	api := httptest.NewServer(authMeOK(4, func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		w.Write([]byte(`{"devices":[]}`))
	}))
	defer api.Close()
	session := newTestSession(t, Config{AuthBaseURL: api.URL}, nil, "Bearer st_x")
	res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "devices_list", Arguments: map[string]any{}})
	if res.IsError || gotURL != "/hour-lab-devices" {
		t.Fatalf("url=%q err=%s", gotURL, toolText(t, res))
	}
}
