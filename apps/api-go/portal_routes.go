package main

import (
	"net/http"
	"time"
)

// registerPortalRoutes registra as rotas do portal admin/professor sob /portal/*.
// Permissões granulares por área (recursos `portal_*` no catálogo de cargos):
//   - Leitura (GET): portalRead("portal_<área>") — admin/professor OU cargo com {área}:read.
//   - Escrita (POST/PATCH): portalWrite("portal_<área>") — admin OU cargo com {área}:write
//     (professor NÃO escreve, igual ao adminGuard anterior).
//   - Exclusão (DELETE): permanece admin-only (adminGuard, +sudoGuard quando destrutivo).
//   - Correção de respostas: portalCorrigir (admin/professor OU cargo com portal_correcao:corrigir).
//   - Transversais (overview, busca de usuários): portalAnyRead (qualquer portal_*:read).
func (s *Server) registerPortalRoutes(mux *http.ServeMux) {
	const min = time.Minute

	mux.HandleFunc("GET /portal/overview", s.portalAnyRead(s.handlePortalOverview))

	// Cursos / módulos / fases → portal_cursos
	mux.HandleFunc("GET /portal/courses", s.portalRead("portal_cursos", s.handlePortalListCourses))
	mux.HandleFunc("POST /portal/courses", s.rateLimit(20, min, s.portalWrite("portal_cursos", s.handlePortalCreateCourse)))
	mux.HandleFunc("GET /portal/courses/{courseId}", s.portalRead("portal_cursos", s.handlePortalGetCourse))
	mux.HandleFunc("PATCH /portal/courses/{courseId}", s.rateLimit(30, min, s.portalWrite("portal_cursos", s.handlePortalUpdateCourse)))
	mux.HandleFunc("DELETE /portal/courses/{courseId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteCourse)))

	mux.HandleFunc("GET /portal/courses/{courseId}/modules", s.portalRead("portal_cursos", s.handlePortalListModules))
	mux.HandleFunc("POST /portal/courses/{courseId}/modules", s.rateLimit(20, min, s.portalWrite("portal_cursos", s.handlePortalCreateModule)))
	mux.HandleFunc("PATCH /portal/modules/{moduleId}", s.rateLimit(30, min, s.portalWrite("portal_cursos", s.handlePortalUpdateModule)))
	mux.HandleFunc("PATCH /portal/modules/{moduleId}/reorder", s.rateLimit(30, min, s.portalWrite("portal_cursos", s.handlePortalReorderModule)))
	mux.HandleFunc("DELETE /portal/modules/{moduleId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteModule)))

	mux.HandleFunc("GET /portal/modules/{moduleId}/phases", s.portalRead("portal_cursos", s.handlePortalListPhases))
	mux.HandleFunc("POST /portal/modules/{moduleId}/phases", s.rateLimit(20, min, s.portalWrite("portal_cursos", s.handlePortalCreatePhase)))
	mux.HandleFunc("PATCH /portal/phases/{phaseId}", s.rateLimit(30, min, s.portalWrite("portal_cursos", s.handlePortalUpdatePhase)))
	mux.HandleFunc("PATCH /portal/phases/{phaseId}/reorder", s.rateLimit(30, min, s.portalWrite("portal_cursos", s.handlePortalReorderPhase)))
	mux.HandleFunc("DELETE /portal/phases/{phaseId}", s.adminGuard(s.sudoGuard(s.handlePortalDeletePhase)))

	// Turmas / matrículas / salas → portal_turmas
	mux.HandleFunc("GET /portal/classes", s.portalRead("portal_turmas", s.handlePortalListClasses))
	mux.HandleFunc("POST /portal/classes", s.rateLimit(20, min, s.portalWrite("portal_turmas", s.handlePortalCreateClass)))
	mux.HandleFunc("GET /portal/classes/{classId}", s.portalRead("portal_turmas", s.handlePortalGetClass))
	mux.HandleFunc("PATCH /portal/classes/{classId}", s.rateLimit(30, min, s.portalWrite("portal_turmas", s.handlePortalUpdateClass)))
	mux.HandleFunc("DELETE /portal/classes/{classId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteClass)))
	mux.HandleFunc("GET /portal/classes/{classId}/students", s.portalRead("portal_turmas", s.handlePortalListClassStudents))
	mux.HandleFunc("POST /portal/classes/{classId}/students", s.rateLimit(20, min, s.portalWrite("portal_turmas", s.handlePortalAddClassStudents)))
	mux.HandleFunc("DELETE /portal/classes/{classId}/students/{studentId}", s.adminGuard(s.handlePortalRemoveClassStudent))
	mux.HandleFunc("GET /portal/classes/{classId}/cronograma", s.portalRead("portal_turmas", s.handlePortalClassCronograma))
	mux.HandleFunc("POST /portal/classes/{classId}/iniciar-fases", s.rateLimit(20, min, s.portalWrite("portal_turmas", s.handlePortalIniciarFases)))

	mux.HandleFunc("GET /portal/classes/{classId}/rooms", s.portalRead("portal_turmas", s.handlePortalListClassRooms))
	mux.HandleFunc("POST /portal/classes/{classId}/rooms", s.rateLimit(20, min, s.portalWrite("portal_turmas", s.handlePortalCreateClassRoom)))
	mux.HandleFunc("PATCH /portal/rooms/{roomId}", s.rateLimit(30, min, s.portalWrite("portal_turmas", s.handlePortalUpdateClassRoom)))
	mux.HandleFunc("PATCH /portal/rooms/{roomId}/status", s.rateLimit(30, min, s.portalWrite("portal_turmas", s.handlePortalUpdateClassRoomStatus)))
	mux.HandleFunc("DELETE /portal/rooms/{roomId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteClassRoom)))

	s.registerPortalContentRoutes(mux)
	s.registerPortalProgressRoutes(mux)
	s.registerPortalRewardsRoutes(mux)
	s.registerPortalNotificationRoutes(mux)
	s.registerPortalRankingsRoutes(mux)
}

// registerPortalRankingsRoutes — leaderboards de notas (% de acerto) e pontos
// (soma de portal_point, concedidos automaticamente ao corrigir respostas em
// portalUpdateAnswer/portalBatchUpdateAnswers). Só leitura: pontos não são
// escritos via API, só pela correção. → portal_rankings.
func (s *Server) registerPortalRankingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /portal/rankings/notas", s.portalRead("portal_rankings", s.handlePortalRankingNotas))
	mux.HandleFunc("GET /portal/rankings/pontos", s.portalRead("portal_rankings", s.handlePortalRankingPontos))
}

// registerPortalRewardsRoutes — Fase 4: medalhas (portal_medalhas) e metas
// (portal_metas). DELETE destrutivo segue admin-only (+sudo). Claim segue
// staffGuard (admin/professor).
func (s *Server) registerPortalRewardsRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Busca de usuários do portal (seletor transversal de alunos)
	mux.HandleFunc("GET /portal/users", s.portalAnyRead(s.handlePortalSearchUsers))

	// Medalhas
	mux.HandleFunc("GET /portal/badges", s.portalRead("portal_medalhas", s.handlePortalListBadges))
	mux.HandleFunc("POST /portal/badges", s.rateLimit(20, min, s.portalWrite("portal_medalhas", s.handlePortalCreateBadge)))
	mux.HandleFunc("PATCH /portal/badges/{badgeId}", s.rateLimit(30, min, s.portalWrite("portal_medalhas", s.handlePortalUpdateBadge)))
	mux.HandleFunc("DELETE /portal/badges/{badgeId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteBadge)))

	mux.HandleFunc("GET /portal/badges/holders", s.portalRead("portal_medalhas", s.handlePortalListHolders))
	mux.HandleFunc("POST /portal/badges/holders", s.rateLimit(30, min, s.portalWrite("portal_medalhas", s.handlePortalAssignBadge)))
	mux.HandleFunc("PATCH /portal/badges/holders/{holderId}", s.rateLimit(30, min, s.portalWrite("portal_medalhas", s.handlePortalUpdateHolder)))
	mux.HandleFunc("DELETE /portal/badges/holders/{holderId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteHolder)))

	// Metas
	mux.HandleFunc("GET /portal/goals", s.portalRead("portal_metas", s.handlePortalListGoals))
	mux.HandleFunc("POST /portal/goals", s.rateLimit(20, min, s.portalWrite("portal_metas", s.handlePortalCreateGoal)))
	mux.HandleFunc("GET /portal/goals/{goalId}", s.portalRead("portal_metas", s.handlePortalGetGoal))
	mux.HandleFunc("PATCH /portal/goals/{goalId}", s.rateLimit(30, min, s.portalWrite("portal_metas", s.handlePortalUpdateGoal)))
	mux.HandleFunc("DELETE /portal/goals/{goalId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteGoal)))

	mux.HandleFunc("GET /portal/goals/rewards", s.portalRead("portal_metas", s.handlePortalListGoalRewards))
	mux.HandleFunc("POST /portal/goals/rewards", s.rateLimit(20, min, s.portalWrite("portal_metas", s.handlePortalCreateGoalReward)))
	mux.HandleFunc("PATCH /portal/goals/rewards/{rewardId}", s.rateLimit(30, min, s.portalWrite("portal_metas", s.handlePortalUpdateGoalReward)))
	mux.HandleFunc("DELETE /portal/goals/rewards/{rewardId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteGoalReward)))

	mux.HandleFunc("GET /portal/goals/students", s.portalRead("portal_metas", s.handlePortalListGoalStudents))
	mux.HandleFunc("POST /portal/goals/students", s.rateLimit(30, min, s.portalWrite("portal_metas", s.handlePortalCreateGoalStudent)))
	mux.HandleFunc("PATCH /portal/goals/students/{goalStudentId}", s.rateLimit(60, min, s.portalWrite("portal_metas", s.handlePortalUpdateGoalStudent)))
	mux.HandleFunc("DELETE /portal/goals/students/{goalStudentId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteGoalStudent)))
	mux.HandleFunc("POST /portal/goals/students/{goalStudentId}/claim", s.rateLimit(30, min, s.staffGuard(s.handlePortalClaimGoalReward)))
}

// registerPortalNotificationRoutes — Fase 4: notificações (portal_notificacoes)
// e auditoria (portal_auditoria, só leitura). /me é autosserviço (qualquer sessão).
func (s *Server) registerPortalNotificationRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Autosserviço (qualquer sessão lê/escreve as PRÓPRIAS notificações)
	mux.HandleFunc("GET /portal/notifications/me", s.authGuard(s.handlePortalMyNotifications))
	mux.HandleFunc("PATCH /portal/notifications/read-all", s.rateLimit(30, min, s.authGuard(s.handlePortalMarkAllRead)))
	mux.HandleFunc("PATCH /portal/notifications/{notificationId}/read", s.rateLimit(60, min, s.authGuard(s.handlePortalMarkNotificationRead)))

	// Templates / dispatches (proxy) → portal_notificacoes
	mux.HandleFunc("GET /portal/notifications/templates", s.portalRead("portal_notificacoes", s.handlePortalListTemplates))
	mux.HandleFunc("POST /portal/notifications/templates", s.rateLimit(20, min, s.portalWrite("portal_notificacoes", s.handlePortalCreateTemplate)))
	mux.HandleFunc("PUT /portal/notifications/templates/{templateId}", s.rateLimit(30, min, s.portalWrite("portal_notificacoes", s.handlePortalUpdateTemplate)))
	mux.HandleFunc("DELETE /portal/notifications/templates/{templateId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteTemplate)))
	mux.HandleFunc("POST /portal/notifications/templates/{templateId}/dispatch", s.rateLimit(10, min, s.portalWrite("portal_notificacoes", s.handlePortalDispatchTemplate)))

	mux.HandleFunc("GET /portal/notifications/dispatches", s.portalRead("portal_notificacoes", s.handlePortalListDispatches))
	mux.HandleFunc("DELETE /portal/notifications/dispatches/{dispatchId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteDispatch)))

	// Auditoria (só leitura)
	mux.HandleFunc("GET /portal/activity-logs", s.portalRead("portal_auditoria", s.handlePortalActivityLogs))
}

// registerPortalProgressRoutes — Fase 3: respostas, correção e progresso →
// portal_correcao. Leitura = read; correção (PATCH /answers) = corrigir
// (admin/professor ou cargo com portal_correcao:corrigir).
func (s *Server) registerPortalProgressRoutes(mux *http.ServeMux) {
	const min = time.Minute

	mux.HandleFunc("GET /portal/exercises/{exerciseId}/answers", s.portalRead("portal_correcao", s.handlePortalExerciseAnswers))
	mux.HandleFunc("GET /portal/exercises/{exerciseId}/answer-students", s.portalRead("portal_correcao", s.handlePortalExerciseAnswerStudents))
	mux.HandleFunc("GET /portal/answers/students", s.portalRead("portal_correcao", s.handlePortalAnswerStudents))
	mux.HandleFunc("GET /portal/students/{studentId}/answered-exercises", s.portalRead("portal_correcao", s.handlePortalStudentAnsweredExercises))
	mux.HandleFunc("PATCH /portal/answers/batch", s.rateLimit(30, min, s.portalCorrigir(s.handlePortalBatchUpdateAnswers)))
	mux.HandleFunc("PATCH /portal/answers/{answerId}", s.rateLimit(60, min, s.portalCorrigir(s.handlePortalUpdateAnswer)))

	mux.HandleFunc("GET /portal/phases/{phaseId}/progress", s.portalRead("portal_correcao", s.handlePortalPhaseProgress))
	mux.HandleFunc("GET /portal/classes/{classId}/progress", s.portalRead("portal_correcao", s.handlePortalClassProgress))
}

// registerPortalContentRoutes — Fase 2: exercícios/containers (portal_exercicios),
// materiais (portal_materiais) e vídeos (portal_videos).
func (s *Server) registerPortalContentRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Exercícios / containers → portal_exercicios
	mux.HandleFunc("GET /portal/phases/{phaseId}/exercises", s.portalRead("portal_exercicios", s.handlePortalListExercises))
	mux.HandleFunc("POST /portal/phases/{phaseId}/exercises", s.rateLimit(20, min, s.portalWrite("portal_exercicios", s.handlePortalCreateExercise)))
	mux.HandleFunc("GET /portal/exercises/{exerciseId}", s.portalRead("portal_exercicios", s.handlePortalGetExercise))
	mux.HandleFunc("PATCH /portal/exercises/{exerciseId}", s.rateLimit(30, min, s.portalWrite("portal_exercicios", s.handlePortalUpdateExercise)))
	mux.HandleFunc("PATCH /portal/exercises/{exerciseId}/reorder", s.rateLimit(30, min, s.portalWrite("portal_exercicios", s.handlePortalReorderExercise)))
	mux.HandleFunc("DELETE /portal/exercises/{exerciseId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteExercise)))
	mux.HandleFunc("GET /portal/exercises/{exerciseId}/container", s.portalRead("portal_exercicios", s.handlePortalExerciseContainer))

	mux.HandleFunc("GET /portal/phases/{phaseId}/containers", s.portalRead("portal_exercicios", s.handlePortalListContainers))
	mux.HandleFunc("POST /portal/containers", s.rateLimit(20, min, s.portalWrite("portal_exercicios", s.handlePortalCreateContainer)))
	mux.HandleFunc("POST /portal/containers/add-exercises", s.rateLimit(20, min, s.portalWrite("portal_exercicios", s.handlePortalAddContainerExercises)))
	mux.HandleFunc("DELETE /portal/containers/group", s.adminGuard(s.sudoGuard(s.handlePortalDeleteContainerGroup)))
	mux.HandleFunc("DELETE /portal/containers/{containerTaskId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteContainerTask)))

	// Materiais → portal_materiais
	mux.HandleFunc("GET /portal/courses/{courseId}/materials", s.portalRead("portal_materiais", s.handlePortalListMaterials))
	mux.HandleFunc("POST /portal/courses/{courseId}/materials", s.rateLimit(20, min, s.portalWrite("portal_materiais", s.handlePortalCreateMaterial)))
	mux.HandleFunc("GET /portal/materials/{materialId}", s.portalRead("portal_materiais", s.handlePortalGetMaterial))
	mux.HandleFunc("PATCH /portal/materials/{materialId}", s.rateLimit(30, min, s.portalWrite("portal_materiais", s.handlePortalUpdateMaterial)))
	mux.HandleFunc("DELETE /portal/materials/{materialId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteMaterial)))

	// Vídeos → portal_videos
	mux.HandleFunc("GET /portal/videos", s.portalRead("portal_videos", s.handlePortalListVideos))
	mux.HandleFunc("POST /portal/videos", s.rateLimit(20, min, s.portalWrite("portal_videos", s.handlePortalCreateVideo)))
	mux.HandleFunc("GET /portal/videos/{videoId}", s.portalRead("portal_videos", s.handlePortalGetVideo))
	mux.HandleFunc("PATCH /portal/videos/{videoId}", s.rateLimit(30, min, s.portalWrite("portal_videos", s.handlePortalUpdateVideo)))
	mux.HandleFunc("DELETE /portal/videos/{videoId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteVideo)))
}
