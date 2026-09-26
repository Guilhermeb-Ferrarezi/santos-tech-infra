package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"santos-tech.com/cameras-go/db"
)

type RecorderService struct {
	q         *db.Queries
	go2rtc    *Go2RTCClient
	cfg       Config
	recordDir string
}

func NewRecorderService(q *db.Queries, go2rtc *Go2RTCClient, cfg Config) *RecorderService {
	dir := filepath.Join(os.TempDir(), "cameras_recordings")
	os.MkdirAll(dir, 0755)

	return &RecorderService{
		q:         q,
		go2rtc:    go2rtc,
		cfg:       cfg,
		recordDir: dir,
	}
}

func (s *RecorderService) Start(ctx context.Context) {
	go s.syncCamerasLoop(ctx)
	go s.uploaderLoop(ctx)
}

func (s *RecorderService) syncCamerasLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Mantém os processos ffmpeg rodando
	activeProcesses := make(map[string]*exec.Cmd)

	for {
		cameras, err := s.q.ListCameras(ctx)
		if err != nil {
			slog.Error("falha ao listar câmeras no worker", "err", err)
			continue
		}

		currentCamIDs := make(map[string]bool)
		for _, cam := range cameras {
			idStr := cam.ID.String()
			currentCamIDs[idStr] = true

			if cam.RecordMode == "off" {
				if cmd, ok := activeProcesses[idStr]; ok {
					cmd.Process.Kill()
					delete(activeProcesses, idStr)
				}
				continue
			}

			if _, ok := activeProcesses[idStr]; !ok {
				// Inicia ffmpeg segmentado
				// Cria a pasta da câmera
				camDir := filepath.Join(s.recordDir, idStr)
				os.MkdirAll(camDir, 0755)

				pass, _ := decryptSymmetric(cam.RtspPasswordEncrypted, []byte(s.cfg.EncryptionKey))
				rtspURL := fmt.Sprintf("rtsp://%s:%s@%s:554/cam/realmonitor?channel=1&subtype=1", cam.RtspUser, pass, cam.Ip)

				// Segmentos de 10 min (600s)
				cmd := exec.CommandContext(ctx, "ffmpeg",
					"-i", rtspURL,
					"-c", "copy",
					"-f", "segment",
					"-segment_time", "600",
					"-reset_timestamps", "1",
					"-strftime", "1",
					filepath.Join(camDir, "%Y-%m-%dT%H-%M-%S.mp4"),
				)

				if err := cmd.Start(); err != nil {
					slog.Error("falha ao iniciar ffmpeg", "cam", idStr, "err", err)
					continue
				}
				activeProcesses[idStr] = cmd
			}
		}

		// Limpa removidas
		for id, cmd := range activeProcesses {
			if !currentCamIDs[id] {
				cmd.Process.Kill()
				delete(activeProcesses, id)
			}
		}

		select {
		case <-ctx.Done():
			for _, cmd := range activeProcesses {
				cmd.Process.Kill()
			}
			return
		case <-ticker.C:
		}
	}
}

func (s *RecorderService) uploaderLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		settings, err := s.q.GetSettings(ctx)
		if err != nil {
			continue
		}
		if settings.DriveRefreshTokenEncrypted == "" {
			continue // Não conectado ao drive
		}

		rt, err := decryptSymmetric(settings.DriveRefreshTokenEncrypted, []byte(s.cfg.EncryptionKey))
		if err != nil {
			continue
		}

		cfg := getOauthConfig(
			os.Getenv("GOOGLE_CLIENT_ID"),
			os.Getenv("GOOGLE_CLIENT_SECRET"),
			os.Getenv("OAUTH_REDIRECT_URL"),
		)
		driveClient := NewDriveClient(ctx, cfg, rt)

		// Percorre os vídeos nas pastas das câmeras e faz upload
		entries, err := os.ReadDir(s.recordDir)
		if err != nil {
			continue
		}
		for _, camDirEntry := range entries {
			if !camDirEntry.IsDir() {
				continue
			}
			camID := camDirEntry.Name()
			camPath := filepath.Join(s.recordDir, camID)

			files, _ := os.ReadDir(camPath)
			for _, file := range files {
				filePath := filepath.Join(camPath, file.Name())
				// Evita subir arquivo que o ffmpeg ainda está escrevendo
				stat, err := os.Stat(filePath)
				if err != nil || time.Since(stat.ModTime()) < 1*time.Minute {
					continue
				}

				// Upload
				driveID, err := driveClient.UploadFile(ctx, filePath, file.Name(), "video/mp4")
				if err != nil {
					slog.Error("falha no upload pro drive", "file", file.Name(), "err", err)
					continue
				}

				var uuid pgtype.UUID
				uuid.Scan(camID)

				// Registra no banco
				s.q.CreateRecording(ctx, db.CreateRecordingParams{
					CameraID:    uuid,
					StartTime:   pgtype.Timestamptz{Time: stat.ModTime().Add(-10 * time.Minute), Valid: true},
					EndTime:     pgtype.Timestamptz{Time: stat.ModTime(), Valid: true},
					SizeBytes:   stat.Size(),
					DriveFileID: driveID,
					HasMotion:   false, // TODO: Detectar movimento
					KeepForever: false,
				})

				// Remove do disco local
				os.Remove(filePath)
			}
		}
	}
}
