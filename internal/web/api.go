package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dvbhub/internal/dvb"
	"dvbhub/internal/epg"
	"dvbhub/internal/scan"
	"dvbhub/internal/store"
	"dvbhub/internal/stream"
)

// ---- status ----

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	queued, active := s.sc.Status()
	var counts map[string]int
	s.st.View(func(st *store.State) {
		counts = map[string]int{"networks": len(st.Networks), "muxes": len(st.Muxes), "services": len(st.Services), "channels": len(st.Channels)}
	})
	writeJSON(w, map[string]any{
		"tuners":        s.tm.Status(r.URL.Query().Get("history") != "0"),
		"subscriptions": s.tm.Subscriptions(),
		"scan":          map[string]any{"queued": queued, "active": active},
		"counts":        counts,
		"epg":           map[string]any{"events": s.guide.Count(), "eitEvents": s.eit.Events.Load(), "xmltv": s.xmltv.Status()},
		"time":          time.Now(),
	})
}

// apiSignal is a compact, script-friendly view of reception per tuner.
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
	for _, t := range s.tm.Status(false) {
		out = append(out, sig{Tuner: t.Key, State: t.State, Mux: t.Mux, Locked: t.Signal.Locked,
			StrengthPct: t.Signal.StrengthPct, StrengthDBm: t.Signal.StrengthDBm, SNRdB: t.Signal.SNRdB,
			SNRPct: t.Signal.SNRPct, BER: t.Signal.BER, UNC: t.Signal.UNC, CCErrors: t.CCErrors, Kbps: t.Kbps,
			Quality: Quality(t.State, t.Signal), Bars: t.Bars})
	}
	writeJSON(w, out)
}

// Quality gives a one-word verdict for the signal.
func Quality(state string, s dvb.Signal) string {
	if state == "idle" {
		return "idle"
	}
	return s.Quality()
}

// muxSignal returns the live reading for a mux if it is tuned, else the stored one.
func muxSignal(live map[string]dvb.Signal, st *store.State, muxID string) *store.SignalSnap {
	if sig, ok := live[muxID]; ok {
		snap := sig.Snapshot()
		snap.Live = true
		return &snap
	}
	if m, ok := st.Muxes[muxID]; ok && m.Signal != nil {
		c := *m.Signal
		return &c
	}
	return nil
}

func (s *Server) apiSystem(w http.ResponseWriter, r *http.Request) {
	var ffmpeg string
	s.st.View(func(st *store.State) { ffmpeg = st.Settings.FFmpeg })
	writeJSON(w, map[string]any{"transcode": stream.Detect(ffmpeg), "hardware": s.tm.Hardware()})
}

// ---- tuners ----

func (s *Server) apiTuners(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.tm.Status(false)) }

func (s *Server) apiPutTuner(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var cfg store.TunerConfig
	if err := readJSON(r, &cfg); err != nil {
		httpError(w, 400, err)
		return
	}
	cfg.Key = key
	err := s.st.Update(func(st *store.State) error {
		if _, ok := st.Tuners[key]; !ok {
			return errNotFound
		}
		st.Tuners[key] = &cfg
		return nil
	})
	if err != nil {
		httpError(w, 404, err)
		return
	}
	writeJSON(w, cfg)
}

func (s *Server) apiRediscover(w http.ResponseWriter, r *http.Request) {
	s.tm.Rediscover()
	writeJSON(w, s.tm.Hardware())
}

func (s *Server) apiDrop(w http.ResponseWriter, r *http.Request) {
	secs, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
	if secs <= 0 {
		secs = 10
	}
	if err := s.tm.SimulateDrop(r.PathValue("key"), time.Duration(secs)*time.Second); err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "seconds": secs})
}

func (s *Server) apiKillSub(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	if !s.tm.KillSubscription(id) {
		httpError(w, 404, errNotFound)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- networks & muxes ----

func (s *Server) apiNetworks(w http.ResponseWriter, r *http.Request) {
	type netOut struct {
		store.Network
		Muxes    int `json:"muxes"`
		Services int `json:"services"`
	}
	out := []netOut{}
	s.st.View(func(st *store.State) {
		for _, n := range st.Networks {
			no := netOut{Network: *n}
			for _, m := range st.Muxes {
				if m.NetworkID == n.ID {
					no.Muxes++
				}
			}
			for _, sv := range st.Services {
				if m, ok := st.Muxes[sv.MuxID]; ok && m.NetworkID == n.ID {
					no.Services++
				}
			}
			out = append(out, no)
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, out)
}

func (s *Server) apiPutNetwork(w http.ResponseWriter, r *http.Request) {
	var n store.Network
	if err := readJSON(r, &n); err != nil {
		httpError(w, 400, err)
		return
	}
	switch n.Type {
	case "dvbt", "dvbc", "dvbs", "atsc", "virtual":
	default:
		httpError(w, 400, errors.New("type must be dvbt, dvbc, dvbs, atsc or virtual"))
		return
	}
	if id := r.PathValue("id"); id != "" {
		n.ID = id
	} else {
		n.ID = store.NewID()
	}
	if strings.TrimSpace(n.Name) == "" {
		n.Name = strings.ToUpper(n.Type) + " network"
	}
	s.st.Update(func(st *store.State) error {
		if old, ok := st.Networks[n.ID]; ok && n.NetworkID == 0 {
			n.NetworkID = old.NetworkID
		}
		st.Networks[n.ID] = &n
		return nil
	})
	writeJSON(w, n)
}

func (s *Server) apiDeleteNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.st.Update(func(st *store.State) error {
		delete(st.Networks, id)
		for mid, m := range st.Muxes {
			if m.NetworkID == id {
				deleteMuxLocked(st, mid)
			}
		}
		return nil
	})
	writeJSON(w, map[string]bool{"ok": true})
}

func deleteMuxLocked(st *store.State, muxID string) {
	delete(st.Muxes, muxID)
	for sid, svc := range st.Services {
		if svc.MuxID == muxID {
			delete(st.Services, sid)
			for _, c := range st.Channels {
				c.Services = removeStr(c.Services, sid)
			}
		}
	}
}

func removeStr(list []string, v string) []string {
	out := list[:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) apiScanNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var ids []string
	s.st.View(func(st *store.State) {
		for mid, m := range st.Muxes {
			if m.NetworkID == id && m.Enabled {
				ids = append(ids, mid)
			}
		}
	})
	s.sc.Enqueue(ids...)
	writeJSON(w, map[string]int{"queued": len(ids)})
}

// apiImportMuxes adds muxes from dvbv5/legacy scan-table text in the body.
func (s *Server) apiImportMuxes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		httpError(w, 400, err)
		return
	}
	tunings, err := dvb.ParseScanFile(string(body))
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var added []string
	err = s.st.Update(func(st *store.State) error {
		net, ok := st.Networks[id]
		if !ok {
			return errNotFound
		}
		for _, t := range tunings {
			dup := false
			for _, m := range st.Muxes {
				if m.NetworkID == net.ID && m.Tuning.FrequencyKHz == t.FrequencyKHz && m.Tuning.Polarization == t.Polarization &&
					m.Tuning.StreamID == t.StreamID {
					dup = true
				}
			}
			if dup {
				continue
			}
			m := &store.Mux{ID: store.NewID(), NetworkID: net.ID, Tuning: t, Enabled: true, ScanStatus: "new"}
			st.Muxes[m.ID] = m
			added = append(added, m.ID)
		}
		return nil
	})
	if err != nil {
		httpError(w, 404, err)
		return
	}
	if r.URL.Query().Get("scan") != "0" {
		s.sc.Enqueue(added...)
	}
	writeJSON(w, map[string]int{"parsed": len(tunings), "added": len(added)})
}

type muxOut struct {
	store.Mux
	Label    string `json:"label"`
	Services int    `json:"services"`
	Network  string `json:"network"`
}

func (s *Server) apiMuxes(w http.ResponseWriter, r *http.Request) {
	netID := r.URL.Query().Get("network")
	live := s.tm.LiveMuxSignals()
	out := []muxOut{}
	s.st.View(func(st *store.State) {
		for _, m := range st.Muxes {
			if netID != "" && m.NetworkID != netID {
				continue
			}
			mo := muxOut{Mux: *m, Label: dvb.DescribeTuning(m.Tuning)}
			mo.Signal = muxSignal(live, st, m.ID)
			if m.File != "" {
				mo.Label = filepath.Base(m.File)
			}
			if n, ok := st.Networks[m.NetworkID]; ok {
				mo.Network = n.Name
			}
			for _, sv := range st.Services {
				if sv.MuxID == m.ID {
					mo.Services++
				}
			}
			out = append(out, mo)
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Network != out[j].Network {
			return out[i].Network < out[j].Network
		}
		if out[i].Tuning.FrequencyKHz != out[j].Tuning.FrequencyKHz {
			return out[i].Tuning.FrequencyKHz < out[j].Tuning.FrequencyKHz
		}
		return out[i].File < out[j].File
	})
	writeJSON(w, out)
}

func (s *Server) apiPutMux(w http.ResponseWriter, r *http.Request) {
	var m store.Mux
	if err := readJSON(r, &m); err != nil {
		httpError(w, 400, err)
		return
	}
	isNew := r.PathValue("id") == ""
	if isNew {
		m.ID = store.NewID()
		m.ScanStatus = "new"
	} else {
		m.ID = r.PathValue("id")
	}
	err := s.st.Update(func(st *store.State) error {
		n, ok := st.Networks[m.NetworkID]
		if !ok {
			return errors.New("unknown network")
		}
		if n.Type == "virtual" {
			if m.File == "" {
				return errors.New("virtual muxes need a file")
			}
			if _, err := os.Stat(m.File); err != nil {
				return err
			}
			m.Tuning = store.Tuning{DeliverySystem: "VIRTUAL", StreamID: -1}
		} else if m.Tuning.FrequencyKHz == 0 || m.Tuning.DeliverySystem == "" {
			return errors.New("frequency and delivery system are required")
		}
		if old, ok := st.Muxes[m.ID]; ok {
			m.TSID, m.ONID, m.ScanStatus, m.ScanError, m.LastScan = old.TSID, old.ONID, old.ScanStatus, old.ScanError, old.LastScan
			m.Signal = old.Signal
		} else if !isNew {
			return errNotFound
		}
		st.Muxes[m.ID] = &m
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if isNew && r.URL.Query().Get("scan") != "0" {
		s.sc.Enqueue(m.ID)
	}
	writeJSON(w, m)
}

func (s *Server) apiDeleteMux(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.st.Update(func(st *store.State) error {
		deleteMuxLocked(st, id)
		return nil
	})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) apiScanMux(w http.ResponseWriter, r *http.Request) {
	s.sc.Enqueue(r.PathValue("id"))
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- services & channels ----

func (s *Server) apiServices(w http.ResponseWriter, r *http.Request) {
	type svcOut struct {
		store.Service
		Mux     string            `json:"mux"`
		Network string            `json:"network"`
		Mapped  []string          `json:"mappedTo"`
		Signal  *store.SignalSnap `json:"signal,omitempty"`
	}
	muxID := r.URL.Query().Get("mux")
	live := s.tm.LiveMuxSignals()
	out := []svcOut{}
	s.st.View(func(st *store.State) {
		mapped := map[string][]string{}
		for _, c := range st.Channels {
			for _, sid := range c.Services {
				mapped[sid] = append(mapped[sid], c.Name)
			}
		}
		for _, sv := range st.Services {
			if muxID != "" && sv.MuxID != muxID {
				continue
			}
			so := svcOut{Service: *sv, Mapped: mapped[sv.ID], Signal: muxSignal(live, st, sv.MuxID)}
			if m, ok := st.Muxes[sv.MuxID]; ok {
				so.Mux = dvb.DescribeTuning(m.Tuning)
				if m.File != "" {
					so.Mux = filepath.Base(m.File)
				}
				if n, ok := st.Networks[m.NetworkID]; ok {
					so.Network = n.Name
				}
			}
			out = append(out, so)
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, out)
}

func (s *Server) apiPutService(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &in); err != nil {
		httpError(w, 400, err)
		return
	}
	err := s.st.Update(func(st *store.State) error {
		sv, ok := st.Services[r.PathValue("id")]
		if !ok {
			return errNotFound
		}
		sv.Enabled = in.Enabled
		return nil
	})
	if err != nil {
		httpError(w, 404, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) apiMap(w http.ResponseWriter, r *http.Request) {
	var opt scan.MapOptions
	if err := readJSON(r, &opt); err != nil {
		httpError(w, 400, err)
		return
	}
	created, merged := scan.MapServices(s.st, opt)
	writeJSON(w, map[string]int{"created": created, "merged": merged})
}

func (s *Server) apiChannels(w http.ResponseWriter, r *http.Request) {
	type chOut struct {
		store.Channel
		ServiceNames []string          `json:"serviceNames"`
		Now          *epg.Event        `json:"now,omitempty"`
		Next         *epg.Event        `json:"next,omitempty"`
		StreamURL    string            `json:"streamUrl"`
		Signal       *store.SignalSnap `json:"signal,omitempty"`
	}
	base := s.baseURL(r)
	now := time.Now()
	live := s.tm.LiveMuxSignals()
	out := []chOut{}
	for _, c := range s.channelsSorted(true) {
		co := chOut{Channel: *c, StreamURL: base + "/stream/channel/" + c.ID}
		s.st.View(func(st *store.State) {
			// Live reading of whichever service is being received, else the primary's last reading.
			for i, sid := range c.Services {
				sv, ok := st.Services[sid]
				if !ok {
					continue
				}
				if _, isLive := live[sv.MuxID]; isLive || (i == 0 && co.Signal == nil) {
					co.Signal = muxSignal(live, st, sv.MuxID)
					if isLive {
						break
					}
				}
			}
			for _, sid := range c.Services {
				name := sid
				if sv, ok := st.Services[sid]; ok {
					name = sv.Name
					if m, ok := st.Muxes[sv.MuxID]; ok {
						if m.File != "" {
							name += " @ " + filepath.Base(m.File)
						} else {
							name += " @ " + dvb.DescribeTuning(m.Tuning)
						}
					}
				}
				co.ServiceNames = append(co.ServiceNames, name)
			}
		})
		co.Now, co.Next = s.guide.NowNext(c.ID, now)
		out = append(out, co)
	}
	writeJSON(w, out)
}

func (s *Server) apiPutChannel(w http.ResponseWriter, r *http.Request) {
	var c store.Channel
	if err := readJSON(r, &c); err != nil {
		httpError(w, 400, err)
		return
	}
	if id := r.PathValue("id"); id != "" {
		c.ID = id
	} else {
		c.ID = store.NewID()
	}
	if strings.TrimSpace(c.Name) == "" {
		httpError(w, 400, errors.New("name is required"))
		return
	}
	err := s.st.Update(func(st *store.State) error {
		for _, sid := range c.Services {
			if _, ok := st.Services[sid]; !ok {
				return fmt.Errorf("unknown service %s", sid)
			}
		}
		if c.Profile != "" {
			if _, ok := st.Profiles[c.Profile]; !ok {
				return fmt.Errorf("unknown profile %s", c.Profile)
			}
		}
		for _, o := range st.Channels {
			if o.ID != c.ID && o.Number == c.Number && o.Minor == c.Minor && o.Enabled && c.Enabled {
				return fmt.Errorf("number %s is already used by %s", epg.GuideNumber(&c), o.Name)
			}
		}
		st.Channels[c.ID] = &c
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, c)
}

func (s *Server) apiDeleteChannel(w http.ResponseWriter, r *http.Request) {
	s.st.Update(func(st *store.State) error {
		delete(st.Channels, r.PathValue("id"))
		return nil
	})
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- profiles ----

func (s *Server) apiProfiles(w http.ResponseWriter, r *http.Request) {
	type profOut struct {
		store.Profile
		Command     string `json:"command"`
		Passthrough bool   `json:"passthrough"`
	}
	var ffmpeg string
	out := []profOut{}
	s.st.View(func(st *store.State) {
		ffmpeg = st.Settings.FFmpeg
		for _, p := range st.Profiles {
			po := profOut{Profile: *p, Passthrough: stream.IsPassthrough(p)}
			if !po.Passthrough {
				po.Command = ffmpeg + " " + strings.Join(stream.BuildArgs(p), " ")
			}
			out = append(out, po)
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Passthrough != out[j].Passthrough {
			return out[i].Passthrough
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, out)
}

var profileID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,40}$`)

func (s *Server) apiPutProfile(w http.ResponseWriter, r *http.Request) {
	var p store.Profile
	if err := readJSON(r, &p); err != nil {
		httpError(w, 400, err)
		return
	}
	p.ID = r.PathValue("id")
	if !profileID.MatchString(p.ID) {
		httpError(w, 400, errors.New("profile id must be lowercase letters, digits, . _ -"))
		return
	}
	switch p.VideoCodec {
	case "copy", "h264_nvenc", "hevc_nvenc", "av1_nvenc", "libx264", "libx265":
	default:
		httpError(w, 400, errors.New("unsupported video codec"))
		return
	}
	switch p.AudioCodec {
	case "copy", "aac", "ac3", "mp2":
	default:
		httpError(w, 400, errors.New("unsupported audio codec"))
		return
	}
	switch p.RateControl {
	case "", "cbr", "vbr", "cq":
	default:
		httpError(w, 400, errors.New("rate control must be cbr, vbr or cq"))
		return
	}
	if p.VideoCodec != "copy" && p.RateControl != "cq" && p.BitrateK <= 0 {
		httpError(w, 400, errors.New("bitrate is required"))
		return
	}
	if strings.ContainsAny(p.Extra, ";&|`$") {
		httpError(w, 400, errors.New("extra arguments may not contain shell characters"))
		return
	}
	if p.Name == "" {
		p.Name = p.ID
	}
	s.st.Update(func(st *store.State) error {
		st.Profiles[p.ID] = &p
		return nil
	})
	writeJSON(w, p)
}

func (s *Server) apiDeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.st.Update(func(st *store.State) error {
		if st.Settings.HDHRProfile == id {
			return errors.New("profile is the default lineup profile")
		}
		delete(st.Profiles, id)
		for _, c := range st.Channels {
			if c.Profile == id {
				c.Profile = ""
			}
		}
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- settings ----

func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	var set store.Settings
	s.st.View(func(st *store.State) { set = st.Settings })
	writeJSON(w, set)
}

func (s *Server) apiPutSettings(w http.ResponseWriter, r *http.Request) {
	var set store.Settings
	if err := readJSON(r, &set); err != nil {
		httpError(w, 400, err)
		return
	}
	err := s.st.Update(func(st *store.State) error {
		if _, ok := st.Profiles[set.HDHRProfile]; !ok {
			return errors.New("unknown default profile")
		}
		if set.DeviceID == "" {
			set.DeviceID = st.Settings.DeviceID
		}
		if set.FFmpeg == "" {
			set.FFmpeg = "ffmpeg"
		}
		if set.VirtualTuners < 0 || set.VirtualTuners > 8 {
			return errors.New("virtual tuners must be 0-8")
		}
		st.Settings = set
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	s.xmltv.Trigger()
	writeJSON(w, set)
}

// ---- EPG ----

func (s *Server) apiEPGGrid(w http.ResponseWriter, r *http.Request) {
	from := time.Now().Truncate(30 * time.Minute)
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 48 {
		hours = 6
	}
	to := from.Add(time.Duration(hours) * time.Hour)
	type row struct {
		ID     string      `json:"id"`
		Number int         `json:"number"`
		Minor  int         `json:"minor,omitempty"`
		Name   string      `json:"name"`
		Icon   string      `json:"icon,omitempty"`
		Source string      `json:"source"`
		Events []epg.Event `json:"events"`
	}
	out := []row{}
	for _, c := range s.channelsSorted(false) {
		src := "eit"
		if c.EPGID != "" {
			src = "xmltv:" + c.EPGID
		}
		out = append(out, row{ID: c.ID, Number: c.Number, Minor: c.Minor, Name: c.Name, Icon: c.Icon, Source: src, Events: s.guide.Range(c.ID, from, to)})
	}
	writeJSON(w, map[string]any{"from": from, "to": to, "channels": out})
}

func (s *Server) apiXMLTVChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"status": s.xmltv.Status(), "channels": s.xmltv.Channels()})
}

func (s *Server) apiXMLTVRefresh(w http.ResponseWriter, r *http.Request) {
	go s.xmltv.Run()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) apiAutoMap(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]int{"mapped": s.xmltv.AutoMap()})
}

// ---- scan tables ----

var scanDirs = []string{"/usr/share/dvb", "/usr/share/dvbv5", "/usr/local/share/dvb"}

func (s *Server) apiScanFiles(w http.ResponseWriter, r *http.Request) {
	out := []string{}
	for _, d := range scanDirs {
		filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err == nil && !e.IsDir() {
				out = append(out, p)
			}
			return nil
		})
	}
	sort.Strings(out)
	writeJSON(w, out)
}

func (s *Server) apiScanFile(w http.ResponseWriter, r *http.Request) {
	p := filepath.Clean(r.URL.Query().Get("path"))
	ok := false
	for _, d := range scanDirs {
		if strings.HasPrefix(p, d+"/") {
			ok = true
		}
	}
	if !ok {
		httpError(w, 400, errors.New("path must be inside a scan table directory"))
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
}
