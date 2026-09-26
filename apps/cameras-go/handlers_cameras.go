package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"santos-tech.com/cameras-go/db"
)

type createCameraReq struct {
	Name              string `json:"name"`
	IP                string `json:"ip"`
	RTSPUser          string `json:"rtsp_user"`
	RTSPPassword      string `json:"rtsp_password"`
	QualityMain       string `json:"quality_main"`
	QualitySub        string `json:"quality_sub"`
	RecordMode        string `json:"record_mode"`
	MotionSensitivity int32  `json:"motion_sensitivity"`
}

func (s *Server) handleListCameras(w http.ResponseWriter, r *http.Request) {
	cameras, err := s.q.ListCameras(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, cameras)
}

func (s *Server) handleCreateCamera(w http.ResponseWriter, r *http.Request) {
	var req createCameraReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "JSON inválido")
		return
	}

	encPassword, err := encryptSymmetric(req.RTSPPassword, []byte(s.cfg.EncryptionKey))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "crypto_error", "Erro ao criptografar senha")
		return
	}

	cam, err := s.q.CreateCamera(r.Context(), db.CreateCameraParams{
		Name:                  req.Name,
		Ip:                    req.IP,
		RtspUser:              req.RTSPUser,
		RtspPasswordEncrypted: encPassword,
		QualityMain:           req.QualityMain,
		QualitySub:            req.QualitySub,
		RecordMode:            req.RecordMode,
		MotionSensitivity:     req.MotionSensitivity,
		IsActive:              true,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	// Registra no go2rtc (Subtype 0 = Main, 1 = Sub)
	rtspURL := fmt.Sprintf("rtsp://%s:%s@%s:554/cam/realmonitor?channel=1&subtype=1", req.RTSPUser, req.RTSPPassword, req.IP)
	if err := s.go2rtc.RegisterStream(cam.ID.String(), rtspURL); err != nil {
		// Log erro, mas não falha a criação
		slog.Error("falha ao registrar câmera no go2rtc", "err", err, "cam_id", cam.ID)
	}

	writeJSON(w, http.StatusCreated, cam)
}

type updateCameraReq struct {
	Name              *string `json:"name"`
	IP                *string `json:"ip"`
	RTSPUser          *string `json:"rtsp_user"`
	RTSPPassword      *string `json:"rtsp_password"`
	QualityMain       *string `json:"quality_main"`
	QualitySub        *string `json:"quality_sub"`
	RecordMode        *string `json:"record_mode"`
	MotionSensitivity *int32  `json:"motion_sensitivity"`
	IsActive          *bool   `json:"is_active"`
}

func (s *Server) handleUpdateCamera(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var id pgtype.UUID
	if err := id.Scan(idStr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID inválido")
		return
	}

	cam, err := s.q.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Câmera não encontrada")
		return
	}

	var req updateCameraReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "JSON inválido")
		return
	}

	if req.Name != nil {
		cam.Name = *req.Name
	}
	if req.IP != nil {
		cam.Ip = *req.IP
	}
	if req.RTSPUser != nil {
		cam.RtspUser = *req.RTSPUser
	}
	if req.RTSPPassword != nil && *req.RTSPPassword != "" {
		encPassword, err := encryptSymmetric(*req.RTSPPassword, []byte(s.cfg.EncryptionKey))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "crypto_error", "Erro ao criptografar senha")
			return
		}
		cam.RtspPasswordEncrypted = encPassword
	}
	if req.QualityMain != nil {
		cam.QualityMain = *req.QualityMain
	}
	if req.QualitySub != nil {
		cam.QualitySub = *req.QualitySub
	}
	if req.RecordMode != nil {
		cam.RecordMode = *req.RecordMode
	}
	if req.MotionSensitivity != nil {
		cam.MotionSensitivity = *req.MotionSensitivity
	}
	if req.IsActive != nil {
		cam.IsActive = *req.IsActive
	}

	updatedCam, err := s.q.UpdateCamera(r.Context(), db.UpdateCameraParams{
		ID:                    cam.ID,
		Name:                  cam.Name,
		Ip:                    cam.Ip,
		RtspUser:              cam.RtspUser,
		RtspPasswordEncrypted: cam.RtspPasswordEncrypted,
		QualityMain:           cam.QualityMain,
		QualitySub:            cam.QualitySub,
		RecordMode:            cam.RecordMode,
		MotionSensitivity:     cam.MotionSensitivity,
		IsActive:              cam.IsActive,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	if updatedCam.IsActive {
		pass, _ := decryptSymmetric(updatedCam.RtspPasswordEncrypted, []byte(s.cfg.EncryptionKey))
		rtspURL := fmt.Sprintf("rtsp://%s:%s@%s:554/cam/realmonitor?channel=1&subtype=1", updatedCam.RtspUser, pass, updatedCam.Ip)
		_ = s.go2rtc.RegisterStream(updatedCam.ID.String(), rtspURL)
	}

	writeJSON(w, http.StatusOK, updatedCam)
}

func (s *Server) handleGetCamera(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var id pgtype.UUID
	if err := id.Scan(idStr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID inválido")
		return
	}

	cam, err := s.q.GetCamera(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Câmera não encontrada")
		return
	}
	writeJSON(w, http.StatusOK, cam)
}

func (s *Server) handleDeleteCamera(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var id pgtype.UUID
	if err := id.Scan(idStr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID inválido")
		return
	}

	if err := s.q.DeleteCamera(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProxyStream(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var id pgtype.UUID
	if err := id.Scan(idStr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID inválido")
		return
	}

	// Como o ID da câmera no banco é usado como `name` no go2rtc
	proxy := s.go2rtc.ProxyStream(idStr)
	proxy(w, r)
}
