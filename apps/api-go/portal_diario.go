package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Diário de aula — o registro que o professor faz de cada aula PARTICULAR
// dada: resumo (ditado ou digitado), arquivos e vídeo, todos no Drive da
// diretoria (pasta "Diário de aulas"). É a única obrigação do professor no
// pós-aula, e por isso tem que caber em dois minutos: sem aprovação, sem
// revisão, sem versão — salvar de novo sobrescreve (uma linha por aula).
// O aluno vê o resultado em "Minhas aulas" (GET /portal/me/sessions).
//
// Vocabulário: "Diário de aula" (professor) e "Minhas aulas" (aluno). Nunca
// "Tarefas" — isso é o kanban da empresa.

// ── DTOs ─────────────────────────────────────────────────────────────────────

// portalDiaryAttachmentDTO é um arquivo do diário. Só a referência: o arquivo
// em si mora no Drive e é servido ao aluno por GET /portal/me/diary/files.
type portalDiaryAttachmentDTO struct {
	DriveFileID string `json:"driveFileId"`
	Name        string `json:"name"`
	MimeType    string `json:"mimeType"`
	Size        int64  `json:"size"`
}

// portalDiaryDTO é o diário como o STAFF vê (com autor e datas).
type portalDiaryDTO struct {
	SessionID   string                     `json:"sessionId"`
	Summary     string                     `json:"summary"`
	Attachments []portalDiaryAttachmentDTO `json:"attachments"`
	// VideoURL: link externo (YouTube, Drive compartilhado…) quando o vídeo
	// não foi enviado como arquivo; VideoDriveFileID: o vídeo subido pro Drive
	// pelo dashboard. Os dois podem coexistir, nenhum é obrigatório.
	VideoURL         *string   `json:"videoUrl"`
	VideoDriveFileID *string   `json:"videoDriveFileId"`
	AuthorEmail      string    `json:"authorEmail"`
	AuthorName       string    `json:"authorName"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// portalMyDiaryDTO é o que chega ao ALUNO de um diário: sem autor nem ids
// internos além dos arquivos (que ele precisa pra montar a URL de download).
type portalMyDiaryDTO struct {
	Summary          string                     `json:"summary"`
	Attachments      []portalDiaryAttachmentDTO `json:"attachments"`
	VideoURL         *string                    `json:"videoUrl"`
	VideoDriveFileID *string                    `json:"videoDriveFileId"`
}

// portalDiarySessionDTO é uma aula na fila de trabalho do professor
// (GET /portal/diary/pending): com ou sem diário — a tela mostra as duas,
// o hasDiary é o que separa "feito" de "pendente".
type portalDiarySessionDTO struct {
	SessionID   string `json:"sessionId"`
	ClassID     string `json:"classId"`
	ClassName   string `json:"className"`
	StudentName string `json:"studentName"`
	Date        string `json:"date"` // AAAA-MM-DD
	StartTime   string `json:"startTime,omitempty"`
	EndTime     string `json:"endTime,omitempty"`
	HasDiary    bool   `json:"hasDiary"`
}

// ── Input ────────────────────────────────────────────────────────────────────

const (
	// portalDiarySummaryMax: 20 mil caracteres é um resumo ditado de uma aula
	// de duas horas com folga — o limite existe pra ninguém colar um livro.
	portalDiarySummaryMax = 20_000
	// portalDiaryAttachmentsMax: mais que 50 arquivos numa aula é sinal de que
	// alguém subiu a pasta errada, não um diário.
	portalDiaryAttachmentsMax = 50
	portalDiaryVideoURLMax    = 2_000
	// portalDiaryTextMax: teto dos campos curtos de cada anexo (id do Drive,
	// nome, mimeType). O Drive não gera nada perto disso; é só pra não gravar
	// lixo arbitrário no JSONB.
	portalDiaryTextMax = 255
	// portalDiaryBodyMax: o PUT do diário aceita mais que os 64 KiB do
	// portalBodyJSON — 20 mil caracteres acentuados/emoji em UTF-8 já passam
	// disso, e os 50 anexos ainda cabem em cima. 256 KiB segura os dois.
	portalDiaryBodyMax = 256 << 10
)

// portalDiaryInput é o corpo do PUT. Não é update parcial: cada save reenvia
// o diário inteiro (é assim que "salvar de novo sobrescreve" funciona sem
// merge). videoUrl/videoDriveFileId ausentes ou vazios = sem vídeo.
type portalDiaryInput struct {
	Summary          string                     `json:"summary"`
	Attachments      []portalDiaryAttachmentDTO `json:"attachments"`
	VideoURL         *string                    `json:"videoUrl,omitempty"`
	VideoDriveFileID *string                    `json:"videoDriveFileId,omitempty"`
}

// validate confere os limites do contrato e NORMALIZA o input: aparas nos
// textos, lista de anexos nunca nil (o JSONB é '[]', não NULL) e string vazia
// nos campos de vídeo vira nil (NULL no banco) — o front manda "" quando a
// pessoa limpa o campo, e "" e "sem vídeo" têm que ser a mesma coisa.
func (in *portalDiaryInput) validate() error {
	if utf8.RuneCountInString(in.Summary) > portalDiarySummaryMax {
		return validationErr(fmt.Sprintf("o resumo deve ter no máximo %d caracteres", portalDiarySummaryMax))
	}
	if in.Attachments == nil {
		in.Attachments = []portalDiaryAttachmentDTO{}
	}
	if len(in.Attachments) > portalDiaryAttachmentsMax {
		return validationErr(fmt.Sprintf("no máximo %d arquivos por aula (recebidos %d)", portalDiaryAttachmentsMax, len(in.Attachments)))
	}
	for i := range in.Attachments {
		a := &in.Attachments[i]
		a.DriveFileID = strings.TrimSpace(a.DriveFileID)
		a.Name = strings.TrimSpace(a.Name)
		a.MimeType = strings.TrimSpace(a.MimeType)
		if a.DriveFileID == "" || a.Name == "" {
			return validationErr(fmt.Sprintf("arquivo %d sem driveFileId ou sem nome", i+1))
		}
		if len(a.DriveFileID) > portalDiaryTextMax || len(a.Name) > portalDiaryTextMax || len(a.MimeType) > portalDiaryTextMax {
			return validationErr(fmt.Sprintf("arquivo %d com driveFileId, nome ou mimeType longo demais", i+1))
		}
		if a.Size < 0 {
			return validationErr(fmt.Sprintf("arquivo %d com tamanho negativo", i+1))
		}
	}
	in.VideoURL = portalDiaryOptionalText(in.VideoURL)
	if in.VideoURL != nil {
		if len(*in.VideoURL) > portalDiaryVideoURLMax {
			return validationErr(fmt.Sprintf("videoUrl deve ter no máximo %d caracteres", portalDiaryVideoURLMax))
		}
		// Só http/https: o aluno abre esse link direto do navegador — um
		// javascript: ou data: aqui seria XSS servido pela API.
		u, err := url.Parse(*in.VideoURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return validationErr("videoUrl deve ser um link http(s) válido")
		}
	}
	in.VideoDriveFileID = portalDiaryOptionalText(in.VideoDriveFileID)
	if in.VideoDriveFileID != nil && len(*in.VideoDriveFileID) > portalDiaryTextMax {
		return validationErr("videoDriveFileId longo demais")
	}
	return nil
}

// portalDiaryOptionalText apara e transforma vazio em nil.
func portalDiaryOptionalText(v *string) *string {
	if v == nil {
		return nil
	}
	t := strings.TrimSpace(*v)
	if t == "" {
		return nil
	}
	return &t
}

// portalDiaryDaysFrom lê ?days= da fila de pendentes: ausente = 30, fora de
// 1..365 é ajustado pro limite (como page/limit em portalPaginationFrom —
// filtro de listagem não derruba a tela), mas texto que não é número é 400.
func portalDiaryDaysFrom(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 30, nil
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return 0, validationErr("days inválido (use um número de 1 a 365)")
	}
	if n < 1 {
		n = 1
	}
	if n > 365 {
		n = 365
	}
	return n, nil
}

// ── Store ────────────────────────────────────────────────────────────────────

// portalSessionExists separa "aula não existe" (404) de "aula sem diário"
// (200 com diary:null) — os dois seriam ErrNoRows numa query só.
func (s *Server) portalSessionExists(ctx context.Context, sessionID int64) error {
	var ok bool
	if err := s.portalDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM class_session WHERE id=$1)`, sessionID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return notFoundErr("Aula")
	}
	return nil
}

// portalGetDiary devolve o diário da aula, ou nil (sem erro) quando ainda não
// foi registrado. Aula inexistente → 404.
func (s *Server) portalGetDiary(ctx context.Context, sessionID int64) (*portalDiaryDTO, error) {
	if err := s.portalSessionExists(ctx, sessionID); err != nil {
		return nil, err
	}
	var d portalDiaryDTO
	var attachments string
	err := s.portalDB.QueryRow(ctx,
		`SELECT session_id::text, summary, attachments::text, video_url, video_drive_file_id,
		        author_email, author_name, created_at, updated_at
		 FROM session_diary WHERE session_id=$1`, sessionID).
		Scan(&d.SessionID, &d.Summary, &attachments, &d.VideoURL, &d.VideoDriveFileID,
			&d.AuthorEmail, &d.AuthorName, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.Attachments = portalDiaryParseAttachments(attachments)
	return &d, nil
}

// portalUpsertDiary grava (ou regrava) o diário da aula. author_* vem da
// sessão de quem salvou — o último a salvar fica como autor, sem histórico.
func (s *Server) portalUpsertDiary(ctx context.Context, sessionID int64, in portalDiaryInput, authorEmail, authorName string) (*portalDiaryDTO, error) {
	if err := s.portalSessionExists(ctx, sessionID); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(in.Attachments)
	if err != nil {
		return nil, err
	}
	var d portalDiaryDTO
	var attachments string
	err = s.portalDB.QueryRow(ctx,
		`INSERT INTO session_diary (session_id, author_email, author_name, summary, attachments, video_url, video_drive_file_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, NOW(), NOW())
		 ON CONFLICT (session_id) DO UPDATE SET
		   author_email=EXCLUDED.author_email, author_name=EXCLUDED.author_name,
		   summary=EXCLUDED.summary, attachments=EXCLUDED.attachments,
		   video_url=EXCLUDED.video_url, video_drive_file_id=EXCLUDED.video_drive_file_id,
		   updated_at=NOW()
		 RETURNING session_id::text, summary, attachments::text, video_url, video_drive_file_id,
		           author_email, author_name, created_at, updated_at`,
		sessionID, strings.ToLower(strings.TrimSpace(authorEmail)), strings.TrimSpace(authorName),
		in.Summary, string(raw), in.VideoURL, in.VideoDriveFileID).
		Scan(&d.SessionID, &d.Summary, &attachments, &d.VideoURL, &d.VideoDriveFileID,
			&d.AuthorEmail, &d.AuthorName, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return nil, portalDBErr(err)
	}
	d.Attachments = portalDiaryParseAttachments(attachments)
	return &d, nil
}

// portalDiaryParseAttachments desserializa o JSONB de anexos. JSON inválido
// (não deveria acontecer — só este código escreve a coluna) vira lista vazia
// em vez de derrubar a leitura do diário inteiro.
func portalDiaryParseAttachments(raw string) []portalDiaryAttachmentDTO {
	out := []portalDiaryAttachmentDTO{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []portalDiaryAttachmentDTO{}
	}
	return out
}

// portalDiaryPending lista as aulas PARTICULARES (class.individual_class) dos
// últimos `days` dias, já dadas (date <= hoje) e não canceladas, mais recente
// primeiro — é a fila de trabalho do professor. Turma de grupo fica de fora
// por enquanto (o diário nasceu pra particular; turma vem numa fase seguinte).
// studentName é o(s) aluno(s) matriculado(s): particular tem um só; se a turma
// ainda não tem matrícula (veio do sync do Notion), cai no nome da turma.
func (s *Server) portalDiaryPending(ctx context.Context, days int) ([]portalDiarySessionDTO, error) {
	rows, err := s.portalDB.Query(ctx,
		`SELECT cs.id::text, cl.id::text, COALESCE(cl.name,''), to_char(cs.date,'YYYY-MM-DD'),
		        COALESCE(to_char(cs.start_time,'HH24:MI'),''), COALESCE(to_char(cs.end_time,'HH24:MI'),''),
		        sd.id IS NOT NULL,
		        COALESCE((SELECT string_agg(NULLIF(u.name,''), ', ' ORDER BY u.name)
		                  FROM enrollment e JOIN "user" u ON u.id = e.user_id
		                  WHERE e.class_id = cl.id), '')
		 FROM class_session cs
		 JOIN class cl ON cl.id = cs.class_id
		 LEFT JOIN session_diary sd ON sd.session_id = cs.id
		 WHERE cl.individual_class AND NOT cs.canceled
		   -- "Hoje" no fuso da escola, não no UTC do servidor: às 22h em Ribeirão
		   -- Preto o CURRENT_DATE já seria amanhã e a fila listaria uma aula que
		   -- ainda não aconteceu (a tela da turma usa a data local do navegador).
		   AND cs.date <= (now() AT TIME ZONE 'America/Sao_Paulo')::date
		   AND cs.date >= (now() AT TIME ZONE 'America/Sao_Paulo')::date - $1::int
		 ORDER BY cs.date DESC, cs.start_time DESC, cs.id DESC`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	itens := []portalDiarySessionDTO{}
	for rows.Next() {
		var d portalDiarySessionDTO
		if err := rows.Scan(&d.SessionID, &d.ClassID, &d.ClassName, &d.Date, &d.StartTime, &d.EndTime, &d.HasDiary, &d.StudentName); err != nil {
			return nil, err
		}
		if d.ClassName == "" {
			d.ClassName = "Turma " + d.ClassID
		}
		if d.StudentName == "" {
			d.StudentName = d.ClassName
		}
		itens = append(itens, d)
	}
	return itens, rows.Err()
}

// portalMyDiaryFileAllowed responde se o aluno (casado por e-mail, como
// portalMyOverview/portalMySessions) pode baixar o arquivo: o fileId tem que
// ser um anexo ou o vídeo de um diário de uma aula de turma em que ele está
// matriculado. É a ÚNICA autorização do download do aluno — a pasta não
// precisa estar em drive_folders, porque a ACL aqui é a matrícula, não a pasta.
func (s *Server) portalMyDiaryFileAllowed(ctx context.Context, email, fileID string) (bool, error) {
	var ok bool
	err := s.portalDB.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM session_diary sd
		   JOIN class_session cs ON cs.id = sd.session_id
		   JOIN enrollment e ON e.class_id = cs.class_id
		   JOIN "user" u ON u.id = e.user_id AND u.email = $1
		   WHERE sd.video_drive_file_id = $2
		      OR EXISTS (SELECT 1 FROM jsonb_array_elements(sd.attachments) a WHERE a->>'driveFileId' = $2)
		 )`, strings.ToLower(email), fileID).Scan(&ok)
	return ok, err
}

// portalDiaryDriveFolderName é o nome cadastrado em drive_folders da pasta em
// que o dashboard sobe os anexos e vídeos do diário (o dashboard usa a mesma
// constante: NOME_PASTA_DIARIO em pastas-drive.ts).
const portalDiaryDriveFolderName = "Diário de aulas"

// driveFolderByName acha a pasta cadastrada pelo nome, tolerando caixa e
// espaços — o nome é digitado à mão em Administração → Pastas do Drive.
func (s *Server) driveFolderByName(ctx context.Context, name string) (*DriveFolder, error) {
	var f DriveFolder
	err := s.db.QueryRow(ctx, `
		SELECT id::text, name, COALESCE(description, ''), upload_account, drive_folder_id, created_by, created_at, updated_at
		FROM drive_folders WHERE lower(trim(name)) = lower(trim($1)) ORDER BY created_at LIMIT 1`, name).
		Scan(&f.ID, &f.Name, &f.Description, &f.UploadAccount, &f.DriveFolderID, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// portalCheckDiaryFiles garante que todo arquivo referenciado pelo diário mora
// DENTRO da pasta "Diário de aulas" do Drive.
//
// Sem isto o diário vira um atalho pra fora da ACL de pastas: o download do
// aluno (/portal/me/diary/files) autoriza só pela matrícula e serve pela conta
// de serviço, então bastaria um professor colar o id do contrato de outro
// aluno (visível em students-overview) num anexo pra esse PDF — com CPF e
// endereço — ficar baixável por quem está na aula. A ancestralidade é a mesma
// checagem que /drive-folders faz (ensureFileInFolder, com cache).
func (s *Server) portalCheckDiaryFiles(ctx context.Context, in portalDiaryInput) error {
	ids := make([]string, 0, len(in.Attachments)+1)
	for _, a := range in.Attachments {
		ids = append(ids, a.DriveFileID)
	}
	if in.VideoDriveFileID != nil && *in.VideoDriveFileID != "" {
		ids = append(ids, *in.VideoDriveFileID)
	}
	if len(ids) == 0 {
		return nil
	}
	if s.drive == nil {
		return appErr(http.StatusServiceUnavailable, "DRIVE_DISABLED", "Arquivos (Google Drive) não configurado")
	}
	folder, err := s.driveFolderByName(ctx, portalDiaryDriveFolderName)
	if err != nil {
		return err
	}
	if folder == nil {
		return validationErr(fmt.Sprintf("pasta %q não cadastrada em Pastas do Drive — cadastre antes de anexar arquivos", portalDiaryDriveFolderName))
	}
	for _, id := range ids {
		if err := s.ensureFileInFolder(ctx, folder, id, false); err != nil {
			var ae *AppError
			if errors.As(err, &ae) && ae.Status == http.StatusForbidden {
				return validationErr(fmt.Sprintf("o arquivo %q não está na pasta %q do Drive", id, portalDiaryDriveFolderName))
			}
			return err
		}
	}
	return nil
}
