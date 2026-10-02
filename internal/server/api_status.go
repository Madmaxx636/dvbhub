package server

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/dvb"
)

// ---- status ----

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{}
	var level string
	s.Store.View(func(st *config.State) {
		counts["networks"], counts["muxes"], counts["services"] = len(st.Networks), len(st.Muxes), len(st.Services)
		for _, c := range st.Channels {
			counts["channels"]++
			if c.Enabled {
				counts["enabledChannels"]++
			}
		}
		level = st.Settings.UILevel
	})
	writeJSON(w, map[string]any{
		"version":         s.Version,
		"uptime":          time.Since(s.started).Round(time.Second).Seconds(),
		"time":            time.Now(),
		"uiLevel":         level,
		"passwordSet":     s.Password != "",
		"hardwareScanned": s.Tuners.HardwareScanned(),
		"tuners":          s.Tuners.Status(r.URL.Query().Get("history") == "1"),
		"subscriptions":   s.Tuners.Subscriptions(),
		"scan":            s.Scanner.Status(),
		"counts":          counts,
		"guide":           map[string]any{"events": s.Guide.Count(), "ota": s.Grabber.Status(), "xmltv": s.XMLTV.Status()},
	})
}

// apiSignal is a compact view of reception per tuner, for scripts.
func (s *Server) apiSignal(w http.ResponseWriter, r *http.Request) {
	type sig struct {
		Tuner       string   `json:"tuner"`
		State       string   `json:"state"`
		Mux         string   `json:"mux,omitempty"`
		Locked      bool     `json:"locked"`
		StrengthPct float64  `json:"strengthPct"`
		StrengthDBm *float64 `json:"strengthDbm,omitempty"`
		SNRdB       *float64 `json:"snrDb,omitempty"`
		SNRPct      float64  `json:"snrPct"`
		BER         float64  `json:"ber"`
		UNC         uint64   `json:"unc"`
		CCErrors    uint64   `json:"ccErrors"`
		Kbps        float64  `json:"kbps"`
		Quality     string   `json:"quality"`
		Bars        int      `json:"bars"`
	}
	out := []sig{}
	for _, t := range s.Tuners.Status(false) {
		out = append(out, sig{Tuner: t.Key, State: t.State, Mux: t.Mux, Locked: t.Signal.Locked,
			StrengthPct: t.Signal.StrengthPct, StrengthDBm: t.Signal.StrengthDBm, SNRdB: t.Signal.SNRdB,
			SNRPct: t.Signal.SNRPct, BER: t.Signal.BER, UNC: t.Signal.UNC, CCErrors: t.CCErrors, Kbps: t.Kbps,
			Quality: t.Quality, Bars: t.Bars})
	}
	writeJSON(w, out)
}

// apiJellyfin lists the URLs to enter in Jellyfin (or Plex).
func (s *Server) apiJellyfin(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	type tuner struct {
		Profile string `json:"profile"`
		Name    string `json:"name"`
		URL     string `json:"url"`
		Default bool   `json:"default"`
	}
	var list []tuner
	s.Store.View(func(st *config.State) {
		for _, p := range st.Profiles {
			t := tuner{Profile: p.ID, Name: p.Name, URL: base + "/p/" + url.PathEscape(p.ID), Default: p.ID == st.Settings.DefaultProfile}
			if t.Default {
				t.URL = base
			}
			list = append(list, t)
		}
	})
	sort.Slice(list, func(i, j int) bool {
		if list[i].Default != list[j].Default {
			return list[i].Default
		}
		return list[i].Name < list[j].Name
	})
	writeJSON(w, map[string]any{"base": base, "tuners": list, "xmltv": base + "/xmltv.xml", "m3u": base + "/playlist.m3u"})
}

// ---- tuners ----

func (s *Server) apiTuners(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Tuners.Status(r.URL.Query().Get("history") == "1"))
}

func (s *Server) apiPutTuner(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	body, err := readBody(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var out config.Tuner
	err = s.Store.Update(func(st *config.State) error {
		old, ok := st.Tuners[key]
		if !ok {
			return errNotFound
		}
		t, err := cloneMerge(old, body)
		if err != nil {
			return err
		}
		t.Key = key
		if t.TuneTimeout < 1 || t.TuneTimeout > 120 {
			return errors.New("lock timeout must be 1-120 seconds")
		}
		for _, n := range t.Networks {
			if _, ok := st.Networks[n]; !ok {
				return errors.New("unknown network " + n)
			}
		}
		st.Tuners[key] = t
		out = *t
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) apiRediscover(w http.ResponseWriter, r *http.Request) {
	s.Tuners.Rediscover()
	writeJSON(w, s.Tuners.Hardware())
}

func (s *Server) apiDrop(w http.ResponseWriter, r *http.Request) {
	secs, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
	if secs <= 0 {
		secs = 10
	}
	if err := s.Tuners.SimulateDrop(r.PathValue("key"), time.Duration(secs)*time.Second); err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "seconds": secs})
}

func (s *Server) apiKillSub(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if !s.Tuners.KillSubscription(id) {
		httpError(w, 404, errNotFound)
		return
	}
	ok(w)
}

// ---- hardware ----

func (s *Server) apiHardware(w http.ResponseWriter, r *http.Request) {
	report := s.Hardware.Report(r.URL.Query().Get("refresh") == "1")
	frontends := s.Tuners.Hardware()
	stuck := 0
	for _, f := range frontends {
		if strings.HasPrefix(f.Name, "not responding") {
			stuck++
		}
	}
	if stuck > 0 {
		report.Problem = true
		report.Summary = strconv.Itoa(stuck) + " tuner(s) don't respond. Their driver is busy or waiting for firmware: install firmware, then replug the tuner or restart the computer."
		if len(report.Actions) == 0 {
			report.Actions = append(report.Actions, "firmware")
		}
	}
	if frontends == nil {
		frontends = []dvb.FrontendInfo{}
	}
	writeJSON(w, map[string]any{
		"report":    report,
		"frontends": frontends,
		"scanned":   s.Tuners.HardwareScanned(),
		"installer": s.Hardware.Installer(),
		"dataDir":   s.Store.Dir(),
		"scriptUrl": "https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/deploy/install-drivers.sh",
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
	if req.Action == "replug" {
		// A driver can hang if its device is reset while in use.
		s.Tuners.ReleaseHardware()
		time.Sleep(time.Second)
	}
	job, err := s.Hardware.Request(req.Action)
	if err != nil {
		httpError(w, 409, err)
		return
	}
	if req.Action == "replug" || req.Action == "firmware" {
		go s.rediscoverAfter(job.ID)
	}
	writeJSON(w, job)
}

// rediscoverAfter looks for tuners again once an installer job has finished.
func (s *Server) rediscoverAfter(id string) {
	for i := 0; i < 360; i++ {
		time.Sleep(5 * time.Second)
		if j, err := s.Hardware.Job(id, false); err == nil && (j.Status == "ok" || j.Status == "failed") {
			time.Sleep(3 * time.Second)
			s.Tuners.Rediscover()
			s.Hardware.Report(true)
			return
		}
	}
}

func (s *Server) apiHardwareJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Hardware.Job(r.PathValue("id"), true)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	writeJSON(w, job)
}

// ---- transcoding ----

func (s *Server) ffmpegPath() string {
	var p string
	s.Store.View(func(st *config.State) { p = st.Settings.FFmpeg })
	return p
}

func (s *Server) apiTranscode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Encoders.Caps(s.ffmpegPath()))
}

func (s *Server) apiTranscodeProbe(w http.ResponseWriter, r *http.Request) {
	go s.Encoders.Probe(s.ffmpegPath())
	time.Sleep(100 * time.Millisecond)
	writeJSON(w, s.Encoders.Caps(s.ffmpegPath()))
}
