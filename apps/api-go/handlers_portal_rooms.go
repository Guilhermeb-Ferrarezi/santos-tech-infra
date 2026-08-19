package main

import (
	"fmt"
	"net/http"
)

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
	p := portalPaginationFrom(r)
	items, total, err := s.portalListClassRooms(r.Context(), classID, p)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPortalPage(items, total, p))
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
	s.portalLogActivity(r, "room_create", "class_room", room.ID, map[string]any{"classId": fmt.Sprint(classID)})
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
	s.portalLogActivity(r, "room_update", "class_room", room.ID, nil)
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
	s.portalLogActivity(r, "room_status_update", "class_room", room.ID, map[string]any{"isAuthorized": *in.IsAuthorized, "status": room.Status})
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
	s.portalLogActivity(r, "room_delete", "class_room", fmt.Sprint(roomID), nil)
	w.WriteHeader(http.StatusNoContent)
}
