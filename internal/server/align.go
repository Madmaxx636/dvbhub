package server

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"dvbhub/internal/dvb"
	"dvbhub/internal/tuners"
)

// Antenna alignment: hold one tuner on one mux and read its signal several
// times a second while an antenna or dish is pointed. The tuner is released
// when the page stops polling for 20 s.

type alignSession struct {
	sub      *tuners.Subscription
	muxID    string
	lastPoll time.Time
}

type aligner struct {
	mu      sync.Mutex
	byTuner map[string]*alignSession
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
		httpError(w, 400, errors.New("choose a tuner and a channel"))
		return
	}
	s.align.mu.Lock()
	if old := s.align.byTuner[req.Tuner]; old != nil {
		old.sub.Close()
		delete(s.align.byTuner, req.Tuner)
	}
	s.align.mu.Unlock()
	sub, err := s.Tuners.Subscribe(tuners.Request{MuxID: req.MuxID, Tuner: req.Tuner, Weight: tuners.WeightLive, Name: "antenna alignment"})
	if err != nil {
		code := 500
		if errors.Is(err, tuners.ErrNoTuner) {
			code = 409
			err = errors.New("that tuner is busy (or can't receive this channel); stop what is using it first")
		}
		httpError(w, code, err)
		return
	}
	as := &alignSession{sub: sub, muxID: req.MuxID, lastPoll: time.Now()}
	s.align.mu.Lock()
	s.align.byTuner[req.Tuner] = as
	s.align.mu.Unlock()
	go func() {
		watchdog := time.NewTicker(2 * time.Second)
		defer watchdog.Stop()
		for {
			select {
			case <-sub.C: // stream data isn't needed
			case <-sub.Done:
				return
			case <-watchdog.C:
				s.align.mu.Lock()
				stale := time.Since(as.lastPoll) > 20*time.Second
				if stale && s.align.byTuner[req.Tuner] == as {
					delete(s.align.byTuner, req.Tuner)
				}
				s.align.mu.Unlock()
				if stale {
					sub.Close()
					return
				}
			}
		}
	}()
	ok(w)
}

func (s *Server) alignPoll(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("tuner")
	s.align.mu.Lock()
	as := s.align.byTuner[key]
	if as != nil {
		as.lastPoll = time.Now()
	}
	s.align.mu.Unlock()
	sig, state, mux, found := s.Tuners.TunerSignal(key)
	if !found {
		httpError(w, 404, errors.New("tuner not found"))
		return
	}
	quality := "idle"
	if state != "idle" {
		quality = sig.Quality()
	}
	writeJSON(w, struct {
		Active  bool       `json:"active"`
		State   string     `json:"state"`
		Mux     string     `json:"mux"`
		Signal  dvb.Signal `json:"signal"`
		Bars    int        `json:"bars"`
		Quality string     `json:"quality"`
	}{as != nil, state, mux, sig, sig.Bars(), quality})
}

func (s *Server) alignStop(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("tuner")
	s.align.mu.Lock()
	as := s.align.byTuner[key]
	delete(s.align.byTuner, key)
	s.align.mu.Unlock()
	if as != nil {
		as.sub.Close()
	}
	ok(w)
}
