package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// rawResponderOutput é a estrutura intermediária para unmarshal do JSON do LLM.
type rawResponderOutput struct {
	Bubbles          []any           `json:"bubbles"`
	Answered         *bool           `json:"answered"`
	AnsweredFromKb   *bool           `json:"answeredFromKb"`
	CitedEntryIDs    []string        `json:"citedEntryIds"`
	Handoff          bool            `json:"handoff"`
	Smalltalk        bool            `json:"smalltalk"`
	ScheduledContact json.RawMessage `json:"scheduledContact"`
	QuotedReplies    json.RawMessage `json:"quotedReplies"`
	KBEntry          *KBEntry        `json:"kbEntry"`
	ClientActions    json.RawMessage `json:"clientActions"`
}

// ParseModelReply extrai e parseia o JSON de resposta do LLM.
// Tolerante a texto ao redor e blocos ```json.
func ParseModelReply(raw string) (ResponderOutput, error) {
	jsonStr := extractJSON(raw)
	if jsonStr == "" {
		return ResponderOutput{}, fmt.Errorf("parser: nenhum objeto JSON encontrado na resposta do LLM")
	}

	var r rawResponderOutput
	if err := json.Unmarshal([]byte(jsonStr), &r); err != nil {
		return ResponderOutput{}, fmt.Errorf("parser: unmarshal JSON: %w", err)
	}

	// Valida campos obrigatórios
	if r.Answered == nil {
		return ResponderOutput{}, fmt.Errorf("parser: campo obrigatório ausente: answered")
	}
	if r.AnsweredFromKb == nil {
		return ResponderOutput{}, fmt.Errorf("parser: campo obrigatório ausente: answeredFromKb")
	}

	// Extrai bubbles (deve ser array não-vazio de strings)
	bubbles, err := extractBubbles(r.Bubbles)
	if err != nil {
		return ResponderOutput{}, fmt.Errorf("parser: %w", err)
	}

	out := ResponderOutput{
		Bubbles:        bubbles,
		Answered:       *r.Answered,
		AnsweredFromKb: *r.AnsweredFromKb,
		CitedEntryIDs:  r.CitedEntryIDs,
		Handoff:        r.Handoff,
		Smalltalk:      r.Smalltalk,
		KBEntry:        r.KBEntry,
	}

	// scheduledContact: descarta se malformado, sem falhar
	if len(r.ScheduledContact) > 0 && string(r.ScheduledContact) != "null" {
		sc := parseScheduledContact(r.ScheduledContact)
		out.ScheduledContact = sc
	}

	// quotedReplies: descarta se malformado, sem falhar
	if len(r.QuotedReplies) > 0 && string(r.QuotedReplies) != "null" {
		qr := parseQuotedReplies(r.QuotedReplies)
		out.QuotedReplies = qr
	}

	// clientActions (modo admin): descarta se malformado, sem falhar
	if len(r.ClientActions) > 0 && string(r.ClientActions) != "null" {
		out.ClientActions = parseClientActions(r.ClientActions)
	}

	return out, nil
}

// parseClientActions tenta parsear clientActions; retorna nil se inválido.
func parseClientActions(raw json.RawMessage) []ClientAction {
	var items []struct {
		PendingID string `json:"pendingId"`
		Draft     string `json:"draft"`
		Send      bool   `json:"send"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	result := make([]ClientAction, 0, len(items))
	for _, it := range items {
		if it.PendingID == "" {
			continue
		}
		result = append(result, ClientAction{
			PendingID: it.PendingID,
			Draft:     it.Draft,
			Send:      it.Send,
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// extractJSON encontra o primeiro '{' e o último '}' no texto, retornando o JSON.
func extractJSON(s string) string {
	// Remove marcadores de bloco de código
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")

	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}
	end := strings.LastIndex(s, "}")
	if end == -1 || end < start {
		return ""
	}
	return s[start : end+1]
}

// extractBubbles converte []any em []string, validando que não está vazio.
func extractBubbles(raw []any) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("campo obrigatório ausente ou vazio: bubbles")
	}
	bubbles := make([]string, 0, len(raw))
	for i, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("bubbles[%d] não é string", i)
		}
		bubbles = append(bubbles, s)
	}
	return bubbles, nil
}

// parseScheduledContact tenta parsear o scheduledContact; retorna nil se inválido.
func parseScheduledContact(raw json.RawMessage) *ScheduledContact {
	var sc struct {
		RawPhrase    string  `json:"rawPhrase"`
		ResolvedDate string  `json:"resolvedDate"`
		Confidence   float64 `json:"confidence"`
	}
	if err := json.Unmarshal(raw, &sc); err != nil {
		return nil
	}
	if sc.RawPhrase == "" || sc.ResolvedDate == "" {
		return nil
	}
	// Valida formato da data
	if _, err := time.Parse("2006-01-02", sc.ResolvedDate); err != nil {
		return nil
	}
	return &ScheduledContact{
		RawPhrase:    sc.RawPhrase,
		ResolvedDate: sc.ResolvedDate,
		Confidence:   sc.Confidence,
	}
}

// parseQuotedReplies tenta parsear quotedReplies; retorna nil se inválido.
func parseQuotedReplies(raw json.RawMessage) []QuotedReply {
	var qrs []struct {
		Bubble int    `json:"bubble"`
		Ref    string `json:"ref"`
	}
	if err := json.Unmarshal(raw, &qrs); err != nil {
		return nil
	}
	result := make([]QuotedReply, 0, len(qrs))
	for _, qr := range qrs {
		if qr.Ref == "" {
			continue
		}
		result = append(result, QuotedReply{
			Bubble: qr.Bubble,
			Ref:    qr.Ref,
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
