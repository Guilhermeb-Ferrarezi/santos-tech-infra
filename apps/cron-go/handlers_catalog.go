package main

import "net/http"

func (s *Server) handleListCatalog(w http.ResponseWriter, r *http.Request) {
	items := make([]CatalogAction, 0, len(Catalog))
	for _, a := range Catalog {
		items = append(items, a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"actions": items})
}
