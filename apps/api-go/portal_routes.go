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
}
