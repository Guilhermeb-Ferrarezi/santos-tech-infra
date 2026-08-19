package main

import "net/http"

func (s *Server) handlePortalListClassRooms(w http.ResponseWriter, r *http.Request) {
	classID, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), classID); err != nil {
		writeErr(w, err)
		return
	}
	items, err := s.portalListClassRooms(r.Context(), classID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handlePortalCreateClassRoom(w http.ResponseWriter, r *http.Request) {
	classID, err := portalPathID(r, "classId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessClass(r.Context(), userIDFrom(r), classID); err != nil {
		writeErr(w, err)
		return
	}
	var in portalRoomInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if err := in.validateCreate(); err != nil {
		writeErr(w, err)
		return
	}
	room, err := s.portalCreateClassRoom(r.Context(), classID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"room": room})
}

func (s *Server) handlePortalUpdateClassRoom(w http.ResponseWriter, r *http.Request) {
	roomID, err := portalPathID(r, "roomId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessRoom(r.Context(), userIDFrom(r), roomID); err != nil {
		writeErr(w, err)
		return
	}
	var in portalRoomInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	room, err := s.portalUpdateClassRoom(r.Context(), roomID, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room})
}

func (s *Server) handlePortalUpdateClassRoomStatus(w http.ResponseWriter, r *http.Request) {
	roomID, err := portalPathID(r, "roomId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalCanAccessRoom(r.Context(), userIDFrom(r), roomID); err != nil {
		writeErr(w, err)
		return
	}
	var in portalRoomStatusInput
	if err := portalBodyJSON(w, r, &in); err != nil {
		writeErr(w, validationErr("corpo inválido"))
		return
	}
	if in.IsAuthorized == nil {
		writeErr(w, validationErr("isAuthorized obrigatório"))
		return
	}
	room, err := s.portalUpdateClassRoomStatus(r.Context(), roomID, *in.IsAuthorized)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"room": room})
}

func (s *Server) handlePortalDeleteClassRoom(w http.ResponseWriter, r *http.Request) {
	roomID, err := portalPathID(r, "roomId")
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.portalDeleteClassRoom(r.Context(), roomID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
