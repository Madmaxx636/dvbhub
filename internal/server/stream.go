package server

import (
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/transcode"
	"dvbhub/internal/tuners"
)

func (s *Server) streamAuto(w http.ResponseWriter, r *http.Request) {
	num := strings.TrimPrefix(r.PathValue("vnum"), "v")
	for _, c := range s.channelsSorted(false) {
		if c.GuideNumber() == num {
			prof, _ := s.lineupProfile(r)
			if r.PathValue("profile") == "" {
				prof = "" // the plain lineup follows each channel's own profile
			}
			s.serveChannel(w, r, c, prof)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) streamChannel(w http.ResponseWriter, r *http.Request) {
	var ch *config.Channel
	s.Store.View(func(st *config.State) {
		if c, ok := st.Channels[r.PathValue("id")]; ok {
			cc := *c
			cc.Services = append([]string(nil), c.Services...)
			ch = &cc
		}
	})
	if ch == nil {
		http.NotFound(w, r)
		return
	}
	s.serveChannel(w, r, ch, "")
}

func (s *Server) streamService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var name string
	s.Store.View(func(st *config.State) {
		if svc, ok := st.Services[id]; ok {
			name = svc.Name
		}
	})
	if name == "" {
		http.NotFound(w, r)
		return
	}
	s.serveChannel(w, r, &config.Channel{ID: "svc:" + id, Name: name, Services: []string{id}, Enabled: true}, "")
}

func clientName(r *http.Request) string {
	ua := r.UserAgent()
	switch {
	case strings.Contains(ua, "Jellyfin"):
		ua = "Jellyfin"
	case strings.Contains(ua, "Lavf"):
		ua = "ffmpeg"
	case strings.Contains(ua, "Plex"):
		ua = "Plex"
	case strings.Contains(ua, "VLC"):
		ua = "VLC"
	}
	if len(ua) > 40 {
		ua = ua[:40]
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return strings.TrimSpace(host + " " + ua)
}

// serveChannel streams a channel. The profile is, in order: ?profile=, the
// one forced by the lineup URL (/p/{profile}), the channel's own, the default.
func (s *Server) serveChannel(w http.ResponseWriter, r *http.Request, ch *config.Channel, forced string) {
	profID := r.URL.Query().Get("profile")
	if profID == "" {
		profID = forced
	}
	if profID == "" {
		profID = ch.Profile
	}
	var prof *config.Profile
	var opt transcode.Options
	var services []string
	s.Store.View(func(st *config.State) {
		if profID == "" {
			profID = st.Settings.DefaultProfile
		}
		if p, ok := st.Profiles[profID]; ok {
			pc := *p
			prof = &pc
		}
		opt.FFmpeg, opt.DefaultKind = st.Settings.FFmpeg, st.Settings.HWAccel
		for _, id := range ch.Services {
			if svc, ok := st.Services[id]; ok && svc.Enabled {
				services = append(services, id)
			}
		}
	})
	if prof == nil {
		http.Error(w, "unknown profile "+profID, http.StatusBadRequest)
		return
	}
	if len(services) == 0 {
		http.Error(w, "this channel has no enabled services", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	client := clientName(r)
	sub, err := s.Tuners.Subscribe(tuners.Request{Services: services, Weight: tuners.WeightLive, Name: ch.Name,
		Client: client, Profile: prof.Name})
	if err != nil {
		code := http.StatusServiceUnavailable
		if !errors.Is(err, tuners.ErrNoTuner) {
			code = http.StatusInternalServerError
		}
		w.Header().Del("Content-Type")
		http.Error(w, err.Error(), code)
		log.Printf("stream: %s for %s refused: %v", ch.Name, client, err)
		return
	}
	defer sub.Close()
	if !prof.Passthrough() {
		opt.Caps = s.Encoders.Caps(opt.FFmpeg)
	}
	log.Printf("stream: %s -> %s [%s]", ch.Name, client, prof.Name)
	start := time.Now()
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}
	flush()
	err = transcode.Serve(r.Context(), sub, prof, opt, w, flush)
	reason := "client closed"
	if err != nil {
		reason = err.Error()
	}
	log.Printf("stream: %s -> %s ended after %s (%s)", ch.Name, client, time.Since(start).Round(time.Second), reason)
}
