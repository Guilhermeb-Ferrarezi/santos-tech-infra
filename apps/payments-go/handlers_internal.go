package main

import "net/http"

// handleInternalGenerateCharges dispara a geração das cobranças mensais sob demanda.
// É idempotente (txid determinístico por recorrência/ciclo), então é seguro re-disparar
// (ex.: retry do cron) sem gerar cobrança em duplicidade. Pensado para ser chamado pelo
// cron-go via conta de serviço (papel Admin) — mesmo guard das demais rotas admin.
func (s *Server) handleInternalGenerateCharges(w http.ResponseWriter, r *http.Request) {
	s.generateMonthlyCharges(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
