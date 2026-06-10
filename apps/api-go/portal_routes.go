package main

import (
	"net/http"
	"time"
)

// registerPortalRoutes registra as rotas do portal admin/professor sob /portal/*.
// Leitura: staffGuard (admin ou professor). Escrita: adminGuard; ações
// destrutivas (DELETE) exigem sudoGuard.
func (s *Server) registerPortalRoutes(mux *http.ServeMux) {
	const min = time.Minute

	mux.HandleFunc("GET /portal/overview", s.staffGuard(s.handlePortalOverview))

	mux.HandleFunc("GET /portal/courses", s.staffGuard(s.handlePortalListCourses))
	mux.HandleFunc("POST /portal/courses", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateCourse)))
	mux.HandleFunc("GET /portal/courses/{courseId}", s.staffGuard(s.handlePortalGetCourse))
	mux.HandleFunc("PATCH /portal/courses/{courseId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateCourse)))
	mux.HandleFunc("DELETE /portal/courses/{courseId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteCourse)))

	mux.HandleFunc("GET /portal/courses/{courseId}/modules", s.staffGuard(s.handlePortalListModules))
	mux.HandleFunc("POST /portal/courses/{courseId}/modules", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateModule)))
	mux.HandleFunc("PATCH /portal/modules/{moduleId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateModule)))
	mux.HandleFunc("PATCH /portal/modules/{moduleId}/reorder", s.rateLimit(30, min, s.adminGuard(s.handlePortalReorderModule)))
	mux.HandleFunc("DELETE /portal/modules/{moduleId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteModule)))

	mux.HandleFunc("GET /portal/modules/{moduleId}/phases", s.staffGuard(s.handlePortalListPhases))
	mux.HandleFunc("POST /portal/modules/{moduleId}/phases", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreatePhase)))
	mux.HandleFunc("PATCH /portal/phases/{phaseId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdatePhase)))
	mux.HandleFunc("PATCH /portal/phases/{phaseId}/reorder", s.rateLimit(30, min, s.adminGuard(s.handlePortalReorderPhase)))
	mux.HandleFunc("DELETE /portal/phases/{phaseId}", s.adminGuard(s.sudoGuard(s.handlePortalDeletePhase)))

	mux.HandleFunc("GET /portal/classes", s.staffGuard(s.handlePortalListClasses))
	mux.HandleFunc("POST /portal/classes", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateClass)))
	mux.HandleFunc("GET /portal/classes/{classId}", s.staffGuard(s.handlePortalGetClass))
	mux.HandleFunc("PATCH /portal/classes/{classId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateClass)))
	mux.HandleFunc("DELETE /portal/classes/{classId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteClass)))
	mux.HandleFunc("GET /portal/classes/{classId}/students", s.staffGuard(s.handlePortalListClassStudents))
	mux.HandleFunc("POST /portal/classes/{classId}/students", s.rateLimit(20, min, s.adminGuard(s.handlePortalAddClassStudents)))
	mux.HandleFunc("DELETE /portal/classes/{classId}/students/{studentId}", s.adminGuard(s.handlePortalRemoveClassStudent))
	mux.HandleFunc("GET /portal/classes/{classId}/cronograma", s.staffGuard(s.handlePortalClassCronograma))
	mux.HandleFunc("POST /portal/classes/{classId}/iniciar-fases", s.rateLimit(20, min, s.adminGuard(s.handlePortalIniciarFases)))

	mux.HandleFunc("GET /portal/classes/{classId}/rooms", s.staffGuard(s.handlePortalListClassRooms))
	mux.HandleFunc("POST /portal/classes/{classId}/rooms", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateClassRoom)))
	mux.HandleFunc("PATCH /portal/rooms/{roomId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateClassRoom)))
	mux.HandleFunc("PATCH /portal/rooms/{roomId}/status", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateClassRoomStatus)))
	mux.HandleFunc("DELETE /portal/rooms/{roomId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteClassRoom)))

	s.registerPortalContentRoutes(mux)
	s.registerPortalProgressRoutes(mux)
	s.registerPortalRewardsRoutes(mux)
	s.registerPortalNotificationRoutes(mux)
}

// registerPortalRewardsRoutes — Fase 4: badges (+holders) e goals (recompensas,
// progresso, resgate). Escrita = adminGuard; exclusão de medalha/meta (cascata)
// = sudoGuard. Resgate (claim) usa staffGuard pois o aluno pode resgatar o
// próprio registro (o store restringe ao próprio user quando role=aluno).
func (s *Server) registerPortalRewardsRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Busca de usuários do portal (seletor de alunos)
	mux.HandleFunc("GET /portal/users", s.staffGuard(s.handlePortalSearchUsers))

	// Badges
	mux.HandleFunc("GET /portal/badges", s.staffGuard(s.handlePortalListBadges))
	mux.HandleFunc("POST /portal/badges", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateBadge)))
	mux.HandleFunc("PATCH /portal/badges/{badgeId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateBadge)))
	mux.HandleFunc("DELETE /portal/badges/{badgeId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteBadge)))

	// Holders (atribuições de medalha)
	mux.HandleFunc("GET /portal/badges/holders", s.staffGuard(s.handlePortalListHolders))
	mux.HandleFunc("POST /portal/badges/holders", s.rateLimit(30, min, s.adminGuard(s.handlePortalAssignBadge)))
	mux.HandleFunc("PATCH /portal/badges/holders/{holderId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateHolder)))
	mux.HandleFunc("DELETE /portal/badges/holders/{holderId}", s.adminGuard(s.handlePortalDeleteHolder))

	// Goals
	mux.HandleFunc("GET /portal/goals", s.staffGuard(s.handlePortalListGoals))
	mux.HandleFunc("POST /portal/goals", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateGoal)))
	mux.HandleFunc("GET /portal/goals/{goalId}", s.staffGuard(s.handlePortalGetGoal))
	mux.HandleFunc("PATCH /portal/goals/{goalId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateGoal)))
	mux.HandleFunc("DELETE /portal/goals/{goalId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteGoal)))

	// Recompensas
	mux.HandleFunc("GET /portal/goals/rewards", s.staffGuard(s.handlePortalListGoalRewards))
	mux.HandleFunc("POST /portal/goals/rewards", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateGoalReward)))
	mux.HandleFunc("PATCH /portal/goals/rewards/{rewardId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateGoalReward)))
	mux.HandleFunc("DELETE /portal/goals/rewards/{rewardId}", s.adminGuard(s.handlePortalDeleteGoalReward))

	// Progresso de alunos / resgate
	mux.HandleFunc("GET /portal/goals/students", s.staffGuard(s.handlePortalListGoalStudents))
	mux.HandleFunc("POST /portal/goals/students", s.rateLimit(30, min, s.adminGuard(s.handlePortalCreateGoalStudent)))
	mux.HandleFunc("PATCH /portal/goals/students/{goalStudentId}", s.rateLimit(60, min, s.adminGuard(s.handlePortalUpdateGoalStudent)))
	mux.HandleFunc("DELETE /portal/goals/students/{goalStudentId}", s.adminGuard(s.handlePortalDeleteGoalStudent))
	mux.HandleFunc("POST /portal/goals/students/{goalStudentId}/claim", s.rateLimit(30, min, s.staffGuard(s.handlePortalClaimGoalReward)))
}

// registerPortalNotificationRoutes — Fase 4: notificações por-usuário (Postgres
// central), templates/dispatches (proxy ao gateway externo) e activity logs.
func (s *Server) registerPortalNotificationRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Autosserviço (qualquer sessão lê/escreve as PRÓPRIAS notificações)
	mux.HandleFunc("GET /portal/notifications/me", s.authGuard(s.handlePortalMyNotifications))
	mux.HandleFunc("PATCH /portal/notifications/read-all", s.rateLimit(30, min, s.authGuard(s.handlePortalMarkAllRead)))
	mux.HandleFunc("PATCH /portal/notifications/{notificationId}/read", s.rateLimit(60, min, s.authGuard(s.handlePortalMarkNotificationRead)))

	// Templates (proxy)
	mux.HandleFunc("GET /portal/notifications/templates", s.staffGuard(s.handlePortalListTemplates))
	mux.HandleFunc("POST /portal/notifications/templates", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateTemplate)))
	mux.HandleFunc("PUT /portal/notifications/templates/{templateId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateTemplate)))
	mux.HandleFunc("DELETE /portal/notifications/templates/{templateId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteTemplate)))
	mux.HandleFunc("POST /portal/notifications/templates/{templateId}/dispatch", s.rateLimit(10, min, s.adminGuard(s.handlePortalDispatchTemplate)))

	// Dispatches (proxy)
	mux.HandleFunc("GET /portal/notifications/dispatches", s.staffGuard(s.handlePortalListDispatches))
	mux.HandleFunc("DELETE /portal/notifications/dispatches/{dispatchId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteDispatch)))

	// Activity logs (auditoria)
	mux.HandleFunc("GET /portal/activity-logs", s.staffGuard(s.handlePortalActivityLogs))
}

// registerPortalProgressRoutes — Fase 3: respostas, correção e progresso.
// Correção (PATCH /answers) usa staffGuard porque avaliar é trabalho do
// professor (alinhado à API antiga), diferente do "admin escreve" das demais.
func (s *Server) registerPortalProgressRoutes(mux *http.ServeMux) {
	const min = time.Minute

	mux.HandleFunc("GET /portal/exercises/{exerciseId}/answers", s.staffGuard(s.handlePortalExerciseAnswers))
	mux.HandleFunc("GET /portal/exercises/{exerciseId}/answer-students", s.staffGuard(s.handlePortalExerciseAnswerStudents))
	mux.HandleFunc("GET /portal/answers/students", s.staffGuard(s.handlePortalAnswerStudents))
	mux.HandleFunc("GET /portal/students/{studentId}/answered-exercises", s.staffGuard(s.handlePortalStudentAnsweredExercises))
	mux.HandleFunc("PATCH /portal/answers/batch", s.rateLimit(30, min, s.staffGuard(s.handlePortalBatchUpdateAnswers)))
	mux.HandleFunc("PATCH /portal/answers/{answerId}", s.rateLimit(60, min, s.staffGuard(s.handlePortalUpdateAnswer)))

	mux.HandleFunc("GET /portal/phases/{phaseId}/progress", s.staffGuard(s.handlePortalPhaseProgress))
	mux.HandleFunc("GET /portal/classes/{classId}/progress", s.staffGuard(s.handlePortalClassProgress))
}

// registerPortalContentRoutes — Fase 2: exercícios, questões, containers,
// materiais e vídeos.
func (s *Server) registerPortalContentRoutes(mux *http.ServeMux) {
	const min = time.Minute

	// Exercícios (questões/opções embutidas no corpo)
	mux.HandleFunc("GET /portal/phases/{phaseId}/exercises", s.staffGuard(s.handlePortalListExercises))
	mux.HandleFunc("POST /portal/phases/{phaseId}/exercises", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateExercise)))
	mux.HandleFunc("GET /portal/exercises/{exerciseId}", s.staffGuard(s.handlePortalGetExercise))
	mux.HandleFunc("PATCH /portal/exercises/{exerciseId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateExercise)))
	mux.HandleFunc("PATCH /portal/exercises/{exerciseId}/reorder", s.rateLimit(30, min, s.adminGuard(s.handlePortalReorderExercise)))
	mux.HandleFunc("DELETE /portal/exercises/{exerciseId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteExercise)))
	mux.HandleFunc("GET /portal/exercises/{exerciseId}/container", s.staffGuard(s.handlePortalExerciseContainer))

	// Containers (agrupadores de exercícios por fase)
	mux.HandleFunc("GET /portal/phases/{phaseId}/containers", s.staffGuard(s.handlePortalListContainers))
	mux.HandleFunc("POST /portal/containers", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateContainer)))
	mux.HandleFunc("POST /portal/containers/add-exercises", s.rateLimit(20, min, s.adminGuard(s.handlePortalAddContainerExercises)))
	mux.HandleFunc("DELETE /portal/containers/group", s.adminGuard(s.sudoGuard(s.handlePortalDeleteContainerGroup)))
	mux.HandleFunc("DELETE /portal/containers/{containerTaskId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteContainerTask)))

	// Materiais (por curso; módulo embutido na description)
	mux.HandleFunc("GET /portal/courses/{courseId}/materials", s.staffGuard(s.handlePortalListMaterials))
	mux.HandleFunc("POST /portal/courses/{courseId}/materials", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateMaterial)))
	mux.HandleFunc("GET /portal/materials/{materialId}", s.staffGuard(s.handlePortalGetMaterial))
	mux.HandleFunc("PATCH /portal/materials/{materialId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateMaterial)))
	mux.HandleFunc("DELETE /portal/materials/{materialId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteMaterial)))

	// Vídeos (módulo embutido na description)
	mux.HandleFunc("GET /portal/videos", s.staffGuard(s.handlePortalListVideos))
	mux.HandleFunc("POST /portal/videos", s.rateLimit(20, min, s.adminGuard(s.handlePortalCreateVideo)))
	mux.HandleFunc("GET /portal/videos/{videoId}", s.staffGuard(s.handlePortalGetVideo))
	mux.HandleFunc("PATCH /portal/videos/{videoId}", s.rateLimit(30, min, s.adminGuard(s.handlePortalUpdateVideo)))
	mux.HandleFunc("DELETE /portal/videos/{videoId}", s.adminGuard(s.sudoGuard(s.handlePortalDeleteVideo)))
}
