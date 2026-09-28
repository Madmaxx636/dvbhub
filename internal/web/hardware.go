package web

import (
	"net/http"

	"dvbhub/internal/hw"
)

func (s *Server) apiHardware(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"report":    s.hw.Report(r.URL.Query().Get("refresh") == "1"),
		"installer": s.hw.Installer(),
		"actions":   hw.Actions,
		"tuners":    s.tm.Status(false),
	})
}

func (s *Server) apiHardwareInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if err := readJSON(r, &req); err != nil {
		httpError(w, 400, err)
		return
	}
	job, err := s.hw.Request(req.Action)
	if err != nil {
		httpError(w, 409, err)
		return
	}
	writeJSON(w, job)
}

func (s *Server) apiHardwareJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.hw.Job(r.PathValue("id"), true)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	writeJSON(w, job)
}
