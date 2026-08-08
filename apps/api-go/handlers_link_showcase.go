package main

import "net/http"

var errLinkShowcaseItemNotFound = appErr(http.StatusNotFound, "LINK_SHOWCASE_ITEM_NOT_FOUND", "Card não encontrado")

func linkShowcaseIDFrom(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !uuidRe.MatchString(id) {
		return "", errLinkShowcaseItemNotFound
	}
	return id, nil
}

// GET /links (admin)
func (s *Server) handleListLinkShowcaseItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.listLinkShowcaseItems(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": items})
}

// POST /links (admin)
func (s *Server) handleCreateLinkShowcaseItem(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in LinkShowcaseItemInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateLinkShowcaseInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	item, err := s.insertLinkShowcaseItem(r.Context(), in, userIDFrom(r))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"link": item})
}

// PUT /links/{id} (admin)
func (s *Server) handleUpdateLinkShowcaseItem(w http.ResponseWriter, r *http.Request) {
	id, err := linkShowcaseIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in LinkShowcaseItemInput
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, appErr(http.StatusBadRequest, "BAD_REQUEST", "Corpo inválido"))
		return
	}
	if err := validateLinkShowcaseInput(&in); err != nil {
		writeErr(w, err)
		return
	}
	item, err := s.updateLinkShowcaseItem(r.Context(), id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	if item == nil {
		writeErr(w, errLinkShowcaseItemNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"link": item})
}

// DELETE /links/{id} (admin)
func (s *Server) handleDeleteLinkShowcaseItem(w http.ResponseWriter, r *http.Request) {
	id, err := linkShowcaseIDFrom(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.deleteLinkShowcaseItem(r.Context(), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /public/links — sem auth, só os cards ativos.
func (s *Server) handleListPublicLinkShowcaseItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.listPublicLinkShowcaseItems(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	views := make([]LinkShowcasePublicItem, len(items))
	for i, item := range items {
		views[i] = toPublicLinkShowcaseView(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": views})
}
