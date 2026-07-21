package main

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Cliente MQTT persistente pro broker de nuvem da Bambu Lab (não depende da
// impressora estar na mesma rede — é o mesmo broker que o app Bambu Handy usa
// fora de casa). Conecta uma vez no boot e fica reconectando sozinho; o status
// chega via push assíncrono no tópico device/{id}/report.
// Protocolo: https://github.com/Doridian/OpenBambuAPI/blob/main/mqtt.md
type bambuClient struct {
	client   mqtt.Client
	deviceID string

	mu   sync.RWMutex
	last map[string]any
}

func newBambuClient(cfg Config) *bambuClient {
	bc := &bambuClient{deviceID: cfg.BambuDeviceID, last: map[string]any{}}

	hostname, _ := os.Hostname()
	opts := mqtt.NewClientOptions().
		AddBroker("tls://us.mqtt.bambulab.com:8883").
		SetUsername("u_" + cfg.BambuUserID).
		SetPassword(cfg.BambuAccessToken).
		// hostname evita client ID duplicado entre réplicas (MQTT derruba a
		// conexão antiga quando um client ID repetido conecta de novo).
		SetClientID("santos-tech-mcp-" + cfg.BambuDeviceID + "-" + hostname).
		SetAutoReconnect(true).
		SetConnectTimeout(10 * time.Second).
		SetOnConnectHandler(func(c mqtt.Client) {
			c.Subscribe("device/"+bc.deviceID+"/report", 0, bc.onMessage)
		})

	bc.client = mqtt.NewClient(opts)
	return bc
}

func (bc *bambuClient) onMessage(_ mqtt.Client, msg mqtt.Message) {
	var envelope struct {
		Print map[string]any `json:"print"`
	}
	if err := json.Unmarshal(msg.Payload(), &envelope); err != nil || envelope.Print == nil {
		return
	}
	bc.mu.Lock()
	for k, v := range envelope.Print {
		bc.last[k] = v
	}
	bc.mu.Unlock()
}

// requestStatus pede um snapshot completo ("pushall"); a resposta chega
// assíncrona em onMessage, não como retorno direto dessa chamada.
func (bc *bambuClient) requestStatus() {
	payload, _ := json.Marshal(map[string]any{
		"pushing": map[string]any{
			"sequence_id": "0",
			"command":     "pushall",
			"version":     1,
			"push_target": 1,
		},
	})
	bc.client.Publish("device/"+bc.deviceID+"/request", 0, false, payload)
}

func (bc *bambuClient) snapshot() map[string]any {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	out := make(map[string]any, len(bc.last))
	for k, v := range bc.last {
		out[k] = v
	}
	return out
}

func (s *Server) addPrinterTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "bambu_status",
		Description: "Status atual da impressora 3D Bambu Lab A1 (via nuvem Bambu — funciona de qualquer rede, " +
			"não só da rede local): estado, progresso, temperaturas do bico/mesa e tempo restante. Só leitura.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, any, error) {
		if s.printer == nil {
			return errResult("ferramenta indisponível: BAMBU_USER_ID/BAMBU_ACCESS_TOKEN/BAMBU_DEVICE_ID não configurados neste MCP."), nil, nil
		}

		s.printer.requestStatus()

		deadline := time.Now().Add(8 * time.Second)
		var raw map[string]any
		for {
			raw = s.printer.snapshot()
			if _, ok := raw["gcode_state"]; ok || time.Now().After(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				return errResult("cancelado"), nil, nil
			case <-time.After(200 * time.Millisecond):
			}
		}

		b, _ := json.Marshal(map[string]any{
			"state":              raw["gcode_state"],
			"progress_percent":   raw["mc_percent"],
			"nozzle_temp_c":      raw["nozzle_temper"],
			"bed_temp_c":         raw["bed_temper"],
			"time_remaining_min": raw["mc_remaining_time"],
		})
		return textResult(string(b)), nil, nil
	})
}
