package web

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"dvbhub/internal/dvb"
	"dvbhub/internal/tuner"
)

// Alignment mode: hold one tuner on one mux and poll its signal several times
// a second while an antenna or dish is being pointed. The session ends when
// the client stops polling for 20 s.

type alignSession struct {
	sub      *tuner.Subscription
	muxID    string
	lastPoll time.Time
}

type aligner struct {
	mu   sync.Mutex
	byTu map[string]*alignSession
}

func (s *Server) alignStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tuner string `json:"tuner"`
		MuxID string `json:"muxId"`
	}
	if err := readJSON(r, &req); err != nil {
		httpError(w, 400, err)
		return
	}
	if req.Tuner == "" || req.MuxID == "" {
		httpError(w, 400, errors.New("tuner and muxId are required"))
		return
	}
	s.align.mu.Lock()
	if old := s.align.byTu[req.Tuner]; old != nil {
		old.sub.Close()
		delete(s.align.byTu, req.Tuner)
	}
	s.align.mu.Unlock()
	sub, err := s.tm.Subscribe(tuner.Request{MuxID: req.MuxID, Tuner: req.Tuner, Weight: tuner.WeightLive, Name: "alignment"})
	if err != nil {
		code := 500
		if errors.Is(err, tuner.ErrNoTuner) {
			code = 409
			err = errors.New("that tuner is busy (or does not support this mux); stop what is using it first")
		}
		httpError(w, code, err)
		return
	}
	as := &alignSession{sub: sub, muxID: req.MuxID, lastPoll: time.Now()}
	s.align.mu.Lock()
	s.align.byTu[req.Tuner] = as
	s.align.mu.Unlock()
	go func() {
		watchdog := time.NewTicker(2 * time.Second)
		defer watchdog.Stop()
		for {
			select {
			case <-sub.C: // discard stream data
			case <-sub.Done:
				return
			case <-watchdog.C:
				s.align.mu.Lock()
				stale := time.Since(as.lastPoll) > 20*time.Second
				if stale && s.align.byTu[req.Tuner] == as {
					delete(s.align.byTu, req.Tuner)
				}
				s.align.mu.Unlock()
				if stale {
					sub.Close()
					return
				}
			}
		}
	}()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) alignPoll(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("tuner")
	s.align.mu.Lock()
	as := s.align.byTu[key]
	if as != nil {
		as.lastPoll = time.Now()
	}
	s.align.mu.Unlock()
	sig, state, mux, ok := s.tm.TunerSignal(key)
	if !ok {
		httpError(w, 404, errors.New("tuner not found"))
		return
	}
	writeJSON(w, struct {
		Active  bool       `json:"active"`
		State   string     `json:"state"`
		Mux     string     `json:"mux"`
		Signal  dvb.Signal `json:"signal"`
		Bars    int        `json:"bars"`
		Quality string     `json:"quality"`
	}{as != nil, state, mux, sig, sig.Bars(), Quality(state, sig)})
}

func (s *Server) alignStop(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("tuner")
	s.align.mu.Lock()
	as := s.align.byTu[key]
	delete(s.align.byTu, key)
	s.align.mu.Unlock()
	if as != nil {
		as.sub.Close()
	}
	writeJSON(w, map[string]bool{"ok": true})
}
