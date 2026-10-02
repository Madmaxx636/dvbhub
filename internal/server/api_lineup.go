package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/dvb"
	"dvbhub/internal/scan"
	"dvbhub/internal/tuners"
)

// ---- plans, scan tables, setup and scan progress ----

func (s *Server) apiPlans(w http.ResponseWriter, r *http.Request) {
	type planOut struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Channels int    `json:"channels"`
	}
	out := []planOut{}
	for _, p := range dvb.Plans() {
		out = append(out, planOut{p.ID, p.Name, p.Type, len(p.Channels)})
	}
	writeJSON(w, out)
}

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
	allowed := false
	for _, d := range scanDirs {
		if strings.HasPrefix(p, d+"/") {
			allowed = true
		}
	}
	if !allowed {
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

func (s *Server) apiSetup(w http.ResponseWriter, r *http.Request) {
	var req scan.SetupRequest
	if err := readJSON(r, &req); err != nil {
		httpError(w, 400, err)
		return
	}
	netID, n, err := s.Scanner.Setup(req)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"networkId": netID, "queued": n})
}

func (s *Server) apiScanStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Scanner.Status())
}

func (s *Server) apiScanCancel(w http.ResponseWriter, r *http.Request) {
	s.Scanner.Cancel(r.URL.Query().Get("network"))
	ok(w)
}

// ---- networks ----

var networkTypes = map[string]bool{"atsc": true, "cable": true, "dvbt": true, "dvbc": true, "dvbs": true, "virtual": true}

func (s *Server) apiNetworks(w http.ResponseWriter, r *http.Request) {
	type netOut struct {
		config.Network
		Muxes    int `json:"muxes"`
		Locked   int `json:"locked"`
		Services int `json:"services"`
	}
	out := []netOut{}
	s.Store.View(func(st *config.State) {
		for _, n := range st.Networks {
			no := netOut{Network: *n}
			for _, m := range st.Muxes {
				if m.NetworkID == n.ID {
					no.Muxes++
					if m.Scan.Status == "ok" {
						no.Locked++
					}
				}
			}
			for _, sv := range st.Services {
				if m, ok := st.Muxes[sv.MuxID]; ok && m.NetworkID == n.ID && sv.Kind != "other" {
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
	body, err := readBody(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	id := r.PathValue("id")
	var out config.Network
	err = s.Store.Update(func(st *config.State) error {
		old := &config.Network{ID: config.NewID(), DiscoverMuxes: true}
		if id != "" {
			o, ok := st.Networks[id]
			if !ok {
				return errNotFound
			}
			old = o
		}
		n, err := cloneMerge(old, body)
		if err != nil {
			return err
		}
		n.ID = old.ID
		if !networkTypes[n.Type] {
			return errors.New("type must be atsc, cable, dvbt, dvbc, dvbs or virtual")
		}
		if strings.TrimSpace(n.Name) == "" {
			n.Name = map[string]string{"atsc": "Antenna (ATSC)", "cable": "Cable (QAM)", "dvbt": "Antenna (DVB-T)",
				"dvbc": "Cable (DVB-C)", "dvbs": "Satellite", "virtual": "Test files"}[n.Type]
		}
		st.Networks[n.ID] = n
		out = *n
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) apiDeleteNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.Scanner.Cancel(id)
	s.Store.Update(func(st *config.State) error {
		delete(st.Networks, id)
		for mid, m := range st.Muxes {
			if m.NetworkID == id {
				deleteMuxLocked(st, mid)
			}
		}
		for _, t := range st.Tuners {
			t.Networks = removeStr(t.Networks, id)
			delete(t.Sat, id)
		}
		return nil
	})
	ok(w)
}

// deleteMuxLocked removes a mux, its services and their channel links, and
// channels left with no service.
func deleteMuxLocked(st *config.State, muxID string) {
	delete(st.Muxes, muxID)
	for sid, svc := range st.Services {
		if svc.MuxID != muxID {
			continue
		}
		delete(st.Services, sid)
		for cid, c := range st.Channels {
			before := len(c.Services)
			c.Services = removeStr(c.Services, sid)
			if before > 0 && len(c.Services) == 0 {
				delete(st.Channels, cid)
			}
		}
	}
}

func removeStr(list []string, v string) []string {
	out := list[:0:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func (s *Server) apiScanNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AutoMap bool             `json:"autoMap"`
		Map     *scan.MapOptions `json:"map"`
	}
	if r.ContentLength > 0 {
		if err := readJSON(r, &req); err != nil {
			httpError(w, 400, err)
			return
		}
	}
	opt := scan.DefaultMapOptions()
	if req.Map != nil {
		opt = *req.Map
	}
	n, err := s.Scanner.ScanNetwork(r.PathValue("id"), req.AutoMap, opt)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, map[string]int{"queued": n})
}

func (s *Server) apiApplyPlan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Plan string `json:"plan"`
		Scan bool   `json:"scan"`
	}
	if err := readJSON(r, &req); err != nil {
		httpError(w, 400, err)
		return
	}
	added, err := scan.ApplyPlan(s.Store, r.PathValue("id"), req.Plan)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if req.Scan {
		s.Scanner.ScanNetwork(r.PathValue("id"), false, scan.DefaultMapOptions())
	}
	writeJSON(w, map[string]int{"added": added})
}

// apiImportMuxes adds muxes from dvbv5 or legacy scan-table text.
func (s *Server) apiImportMuxes(w http.ResponseWriter, r *http.Request) {
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
	added, err := scan.AddTunings(s.Store, r.PathValue("id"), tunings)
	if err != nil {
		httpError(w, 404, err)
		return
	}
	if r.URL.Query().Get("scan") != "0" && len(added) > 0 {
		s.Scanner.ScanNetwork(r.PathValue("id"), false, scan.DefaultMapOptions())
	}
	writeJSON(w, map[string]int{"parsed": len(tunings), "added": len(added)})
}

// ---- muxes ----

// muxSignal returns the live reading for a mux being received, else the stored one.
func muxSignal(live map[string]dvb.Signal, st *config.State, muxID string) *dvb.SignalSnap {
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

func (s *Server) apiMuxes(w http.ResponseWriter, r *http.Request) {
	netID := r.URL.Query().Get("network")
	type muxOut struct {
		config.Mux
		Name     string          `json:"name"`
		Services int             `json:"services"`
		Network  string          `json:"network"`
		Signal   *dvb.SignalSnap `json:"signal,omitempty"`
	}
	live := s.Tuners.LiveMuxSignals()
	out := []muxOut{}
	s.Store.View(func(st *config.State) {
		count := map[string]int{}
		for _, sv := range st.Services {
			if sv.Kind != "other" {
				count[sv.MuxID]++
			}
		}
		for _, m := range st.Muxes {
			if netID != "" && m.NetworkID != netID {
				continue
			}
			mo := muxOut{Mux: *m, Name: tuners.MuxLabel(*m), Services: count[m.ID], Signal: muxSignal(live, st, m.ID)}
			if n, ok := st.Networks[m.NetworkID]; ok {
				mo.Network = n.Name
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
	body, err := readBody(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	id := r.PathValue("id")
	isNew := id == ""
	var out config.Mux
	err = s.Store.Update(func(st *config.State) error {
		old := &config.Mux{ID: config.NewID(), Enabled: true, Scan: config.ScanState{Status: "new"}, Tuning: dvb.Tuning{StreamID: -1}}
		if !isNew {
			o, ok := st.Muxes[id]
			if !ok {
				return errNotFound
			}
			old = o
		}
		m, err := cloneMerge(old, body)
		if err != nil {
			return err
		}
		// Scan results belong to the scanner.
		m.ID, m.Scan, m.Signal, m.TSID, m.ONID = old.ID, old.Scan, old.Signal, old.TSID, old.ONID
		n, ok := st.Networks[m.NetworkID]
		if !ok {
			return errors.New("unknown network")
		}
		if n.Type == "virtual" {
			if m.File == "" {
				return errors.New("a test-file mux needs a file")
			}
			if _, err := os.Stat(m.File); err != nil {
				return err
			}
			m.Tuning = dvb.Tuning{DeliverySystem: "VIRTUAL", StreamID: -1}
		} else if m.Tuning.FrequencyKHz == 0 || m.Tuning.DeliverySystem == "" {
			return errors.New("frequency and delivery system are required")
		}
		st.Muxes[m.ID] = m
		out = *m
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if isNew && r.URL.Query().Get("scan") != "0" {
		s.Scanner.Enqueue(out.ID)
	}
	writeJSON(w, out)
}

func (s *Server) apiDeleteMux(w http.ResponseWriter, r *http.Request) {
	s.Store.Update(func(st *config.State) error {
		deleteMuxLocked(st, r.PathValue("id"))
		return nil
	})
	ok(w)
}

func (s *Server) apiScanMux(w http.ResponseWriter, r *http.Request) {
	s.Scanner.Enqueue(r.PathValue("id"))
	ok(w)
}

// ---- services ----

func (s *Server) apiServices(w http.ResponseWriter, r *http.Request) {
	type svcOut struct {
		config.Service
		Mux     string          `json:"mux"`
		Network string          `json:"network"`
		Mapped  []string        `json:"mappedTo"`
		Signal  *dvb.SignalSnap `json:"signal,omitempty"`
	}
	muxID := r.URL.Query().Get("mux")
	live := s.Tuners.LiveMuxSignals()
	out := []svcOut{}
	s.Store.View(func(st *config.State) {
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
			if so.Mapped == nil {
				so.Mapped = []string{}
			}
			if m, ok := st.Muxes[sv.MuxID]; ok {
				so.Mux = tuners.MuxLabel(*m)
				if n, ok := st.Networks[m.NetworkID]; ok {
					so.Network = n.Name
				}
			}
			out = append(out, so)
		}
	})
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Major == 0) != (b.Major == 0) {
			return a.Major != 0
		}
		if a.Major != b.Major {
			return a.Major < b.Major
		}
		if a.Minor != b.Minor {
			return a.Minor < b.Minor
		}
		if a.Name != b.Name {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.ID < b.ID
	})
	writeJSON(w, out)
}

func (s *Server) apiPutService(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &in); err != nil {
		httpError(w, 400, err)
		return
	}
	err := s.Store.Update(func(st *config.State) error {
		sv, ok := st.Services[r.PathValue("id")]
		if !ok {
			return errNotFound
		}
		if in.Enabled != nil {
			sv.Enabled = *in.Enabled
		}
		return nil
	})
	if err != nil {
		httpError(w, 404, err)
		return
	}
	ok(w)
}

func (s *Server) apiMap(w http.ResponseWriter, r *http.Request) {
	opt := scan.DefaultMapOptions()
	if err := readJSON(r, &opt); err != nil {
		httpError(w, 400, err)
		return
	}
	created, merged := scan.MapServices(s.Store, opt)
	writeJSON(w, map[string]int{"created": created, "merged": merged})
}

// ---- channels ----

func (s *Server) apiChannels(w http.ResponseWriter, r *http.Request) {
	type svcRef struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Mux     string `json:"mux"`
		Enabled bool   `json:"enabled"`
		Video   string `json:"video"`
	}
	type chOut struct {
		config.Channel
		GuideNumber string          `json:"guideNumber"`
		ServiceInfo []svcRef        `json:"serviceInfo"`
		Now         any             `json:"now,omitempty"`
		Next        any             `json:"next,omitempty"`
		StreamURL   string          `json:"streamUrl"`
		Signal      *dvb.SignalSnap `json:"signal,omitempty"`
		Watching    int             `json:"watching"`
	}
	base := s.baseURL(r)
	now := time.Now()
	live := s.Tuners.LiveMuxSignals()
	watching := map[string]int{}
	for _, sub := range s.Tuners.Subscriptions() {
		watching[sub.Name]++
	}
	out := []chOut{}
	for _, c := range s.channelsSorted(true) {
		co := chOut{Channel: *c, GuideNumber: c.GuideNumber(), StreamURL: base + "/stream/channel/" + c.ID,
			ServiceInfo: []svcRef{}, Watching: watching[c.Name]}
		s.Store.View(func(st *config.State) {
			for i, sid := range c.Services {
				ref := svcRef{ID: sid, Name: sid}
				if sv, ok := st.Services[sid]; ok {
					ref.Name, ref.Enabled, ref.Video = sv.Name, sv.Enabled, sv.VideoCodec()
					if m, ok := st.Muxes[sv.MuxID]; ok {
						ref.Mux = tuners.MuxLabel(*m)
					}
					// Live reading of whichever service is being received, else the main one's last reading.
					if _, isLive := live[sv.MuxID]; isLive || (i == 0 && co.Signal == nil) {
						if !(co.Signal != nil && co.Signal.Live) {
							co.Signal = muxSignal(live, st, sv.MuxID)
						}
					}
				}
				co.ServiceInfo = append(co.ServiceInfo, ref)
			}
		})
		if n, x := s.Guide.NowNext(c.ID, now); n != nil || x != nil {
			if n != nil {
				co.Now = n
			}
			if x != nil {
				co.Next = x
			}
		}
		out = append(out, co)
	}
	writeJSON(w, out)
}

func (s *Server) apiPutChannel(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	id := r.PathValue("id")
	var out config.Channel
	err = s.Store.Update(func(st *config.State) error {
		old := &config.Channel{ID: config.NewID(), Enabled: true}
		if id != "" {
			o, ok := st.Channels[id]
			if !ok {
				return errNotFound
			}
			old = o
		}
		c, err := cloneMerge(old, body)
		if err != nil {
			return err
		}
		c.ID = old.ID
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" {
			return errors.New("the channel needs a name")
		}
		if c.Number < 0 || c.Minor < 0 {
			return errors.New("channel numbers can't be negative")
		}
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
				return fmt.Errorf("number %s is already used by %s", c.GuideNumber(), o.Name)
			}
		}
		st.Channels[c.ID] = c
		out = *c
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) apiDeleteChannel(w http.ResponseWriter, r *http.Request) {
	s.Store.Update(func(st *config.State) error {
		delete(st.Channels, r.PathValue("id"))
		return nil
	})
	ok(w)
}

// apiChannelsBulk applies one action to several channels: enable, disable,
// delete, profile (value = profile id or ""), renumber (value = first number),
// or deleteAll (ids ignored).
func (s *Server) apiChannelsBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
		Value  string   `json:"value"`
	}
	if err := readJSON(r, &req); err != nil {
		httpError(w, 400, err)
		return
	}
	n := 0
	err := s.Store.Update(func(st *config.State) error {
		if req.Action == "deleteAll" {
			n = len(st.Channels)
			st.Channels = map[string]*config.Channel{}
			return nil
		}
		if req.Action == "profile" && req.Value != "" {
			if _, ok := st.Profiles[req.Value]; !ok {
				return fmt.Errorf("unknown profile %s", req.Value)
			}
		}
		next := 0
		if req.Action == "renumber" {
			fmt.Sscanf(req.Value, "%d", &next)
			if next <= 0 {
				next = 1
			}
		}
		for _, id := range req.IDs {
			c, ok := st.Channels[id]
			if !ok {
				continue
			}
			n++
			switch req.Action {
			case "enable":
				c.Enabled = true
			case "disable":
				c.Enabled = false
			case "delete":
				delete(st.Channels, id)
			case "profile":
				c.Profile = req.Value
			case "renumber":
				c.Number, c.Minor = next, 0
				next++
			default:
				return fmt.Errorf("unknown action %q", req.Action)
			}
		}
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, map[string]int{"changed": n})
}
