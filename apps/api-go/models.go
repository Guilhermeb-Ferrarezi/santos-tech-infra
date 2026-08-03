package main

import (
	"encoding/json"
	"time"
)

// Papéis: 1=Student, 2=Teacher, 3=Admin, 4=Custom
const (
	RoleStudent = 1
	RoleTeacher = 2
	RoleAdmin   = 3
	RoleCustom  = 4
)

type User struct {
	ID              int64
	Email           string
	Username        *string
	Name            string
	PasswordHash    *string `json:"-"` // nunca serializar (cache Redis, logs, respostas)
	AvatarURL       *string
	Role            int16
	CustomRoleID    *string
	MFAEnabled      bool
	TOTPSecret      *string `json:"-"`             // seed TOTP — nunca serializar
	HasTOTPSecret   bool    `json:"hasTotpSecret"` // derivado de TOTPSecret; sobrevive ao cache Redis
	SuspendedAt     *time.Time
	CreatedAt       time.Time
	Preferences     json.RawMessage // JSONB de preferências de UI (free-form)
	EmailVerifiedAt *time.Time      // quando o usuário confirmou o próprio email (nil = nunca)
	MFAMethod       string          // método 2FA preferido: 'totp' (app autenticador) ou 'email'
	QuotaBytes      int64           // limite de armazenamento da caixa (mailbox), em bytes
	LoginDisabled   bool            // true = caixa compartilhada institucional, sem login por nenhum caminho
}

// UserProfile é o que vai pro cliente (sem dados sensíveis).
type UserProfile struct {
	ID            int64               `json:"id"`
	Email         string              `json:"email"`
	Username      *string             `json:"username"`
	Name          string              `json:"name"`
	Role          int16               `json:"role"`
	CustomRoleID  *string             `json:"customRoleId"`
	AvatarURL     *string             `json:"avatarUrl"`
	MFAEnabled    bool                `json:"mfaEnabled"`
	EmailVerified bool                `json:"emailVerified"`
	MFAMethod     string              `json:"mfaMethod"`
	MFATotp       bool                `json:"mfaTotp"`  // autenticador configurado (totp_secret presente)
	MFAEmail      bool                `json:"mfaEmail"` // 2FA por email disponível (email verificado)
	SuspendedAt   *string             `json:"suspendedAt"`
	Permissions   map[string][]string `json:"permissions"`
	CreatedAt     string              `json:"createdAt"`
	Preferences   json.RawMessage     `json:"preferences"`
}
