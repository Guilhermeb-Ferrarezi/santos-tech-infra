package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tools dos PCs do laboratório (api-go /hour-lab-devices). Permissão é da API
// (permGuard dispositivos:ver/controlar/executar) — aqui só repassamos o token
// do usuário. Saídas de PC (títulos de janela, stdout de comando) são texto de
// terceiros → proxyUntrusted.

var deviceIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

// devicePollInterval/deviceRunWait: variáveis pra os testes encurtarem.
var (
	devicePollInterval = time.Second
	deviceRunWait      = 45 * time.Second
)

type deviceIDInput struct {
	ID string `json:"id" jsonschema:"id do PC (campo id de devices_list)"`
}

type deviceControlInput struct {
	ID     string `json:"id" jsonschema:"id do PC"`
	Action string `json:"action" jsonschema:"message | lock | restart | shutdown"`
	Text   string `json:"text,omitempty" jsonschema:"texto do aviso (só para action=message, até 300 caracteres)"`
}

type deviceCommandRunInput struct {
	ID         string `json:"id" jsonschema:"id do PC"`
	PowerShell string `json:"powershell" jsonschema:"comando PowerShell (até 4000 caracteres)"`
}

type deviceCommandResultInput struct {
	ID        string `json:"id" jsonschema:"id do PC"`
	CommandID string `json:"commandId" jsonschema:"commandId devolvido por device_command_run"`
}

func (s *Server) addDeviceTools(srv *mcp.Server) {
	base := s.cfg.AuthBaseURL

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "devices_list",
		Description: "Lista os PCs do laboratório (nome, online/último contato, CPU/RAM/GPU, apps abertos). Exige permissão dispositivos:ver.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		return s.proxyUntrusted(ctx, req, "GET", base+"/hour-lab-devices", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "device_commands",
		Description: "Histórico auditado de um PC: comandos (com resultado), travar/reiniciar/desligar/aviso e sessões de shell — quem fez, quando e por onde. Exige dispositivos:ver.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deviceIDInput) (*mcp.CallToolResult, any, error) {
		if !deviceIDRe.MatchString(in.ID) {
			return errResult("id inválido"), nil, nil
		}
		return s.proxyUntrusted(ctx, req, "GET", base+"/hour-lab-devices/"+in.ID+"/commands", nil)
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "device_control",
		Description: "Ação num PC: message (aviso na tela), lock (trava a sessão), restart, shutdown. Chega em ~1s se o PC estiver ligado. Fica auditado. Exige dispositivos:controlar.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deviceControlInput) (*mcp.CallToolResult, any, error) {
		if !deviceIDRe.MatchString(in.ID) {
			return errResult("id inválido"), nil, nil
		}
		switch in.Action {
		case "lock", "restart", "shutdown":
			return s.proxy(ctx, req, "POST", base+"/hour-lab-devices/"+in.ID+"/"+in.Action, nil)
		case "message":
			if in.Text == "" || len([]rune(in.Text)) > 300 {
				return errResult("message exige text com até 300 caracteres"), nil, nil
			}
			return s.proxy(ctx, req, "POST", base+"/hour-lab-devices/"+in.ID+"/message", map[string]string{"text": in.Text})
		default:
			return errResult("action deve ser message, lock, restart ou shutdown"), nil, nil
		}
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name: "device_command_run",
		Description: "Executa PowerShell num PC do laboratório COMO SYSTEM (acesso total à máquina) e devolve a saída. " +
			"Espera até 45s; se o PC não responder a tempo, devolve o commandId pra consultar com device_command_result. " +
			"Cada comando fica gravado (quem, PC, texto, resultado). Exige dispositivos:executar. Timeout no PC: 60s.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deviceCommandRunInput) (*mcp.CallToolResult, any, error) {
		if !deviceIDRe.MatchString(in.ID) {
			return errResult("id inválido"), nil, nil
		}
		if in.PowerShell == "" || len(in.PowerShell) > 4000 {
			return errResult("powershell obrigatório (até 4000 caracteres)"), nil, nil
		}
		auth := authorization(req.Extra)
		ctx, cancel := context.WithTimeout(ctx, deviceRunWait+10*time.Second)
		defer cancel()
		enqURL := base + "/hour-lab-devices/" + in.ID + "/command"
		status, raw, err := s.client.do(ctx, "POST", enqURL, auth, map[string]string{"text": in.PowerShell})
		if err != nil || status >= 400 {
			return resultFromMode("POST", enqURL, status, raw, err, false)
		}
		var enq struct {
			CommandID string `json:"commandId"`
		}
		if json.Unmarshal(raw, &enq) != nil || enq.CommandID == "" {
			return errResult("resposta inesperada ao enfileirar: " + string(raw)), nil, nil
		}
		getURL := base + "/hour-lab-devices/" + in.ID + "/commands/" + enq.CommandID
		deadline := time.Now().Add(deviceRunWait)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return textResult(fmt.Sprintf(`{"commandId":%q,"status":"aguardando"}`, enq.CommandID)), nil, nil
			case <-time.After(devicePollInterval):
			}
			st, body, err := s.client.do(ctx, "GET", getURL, auth, nil)
			if err != nil || st >= 400 {
				continue
			}
			var got struct {
				Command struct {
					Result *string `json:"result"`
				} `json:"command"`
			}
			if json.Unmarshal(body, &got) == nil && got.Command.Result != nil {
				return resultFromMode("GET", getURL, st, body, nil, true)
			}
		}
		return textResult(fmt.Sprintf(`{"commandId":%q,"status":"aguardando","dica":"PC pode estar desligado ou ocupado; use device_command_result"}`, enq.CommandID)), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "device_command_result",
		Description: "Consulta o resultado de um comando enviado por device_command_run (result null = ainda não voltou). Exige dispositivos:ver.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deviceCommandResultInput) (*mcp.CallToolResult, any, error) {
		if !deviceIDRe.MatchString(in.ID) || !deviceIDRe.MatchString(in.CommandID) {
			return errResult("id/commandId inválido"), nil, nil
		}
		return s.proxyUntrusted(ctx, req, "GET", base+"/hour-lab-devices/"+in.ID+"/commands/"+in.CommandID, nil)
	})
}
