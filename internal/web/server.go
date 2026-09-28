// Package web serves the admin UI and JSON API, the HDHomeRun emulation,
// M3U/XMLTV lineups and the live streams.
package web

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strings"

	"dvbhub/internal/epg"
	"dvbhub/internal/hw"
	"dvbhub/internal/scan"
	"dvbhub/internal/store"
	"dvbhub/internal/tuner"
)

//go:embed ui
var uiFS embed.FS

type Server struct {
	st       *store.Store
	tm       *tuner.Manager
	sc       *scan.Scanner
	guide    *epg.Guide
	xmltv    *epg.Importer
	eit      *epg.EITGrabber
	password string
	mux      *http.ServeMux
	align    aligner
	hw       *hw.Detector
}

func New(st *store.Store, tm *tuner.Manager, sc *scan.Scanner, g *epg.Guide, im *epg.Importer, eit *epg.EITGrabber, password string) *Server {
	s := &Server{st: st, tm: tm, sc: sc, guide: g, xmltv: im, eit: eit, password: password, mux: http.NewServeMux(),
		align: aligner{byTu: map[string]*alignSession{}}, hw: hw.NewDetector(st.Dir())}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// admin wraps handlers that need the admin password (if one is set).
func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.password != "" {
			_, pw, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(pw), []byte(s.password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="dvbhub"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h(w, r)
	}
}

func (s *Server) routes() {
	m := s.mux
	ui, _ := fs.Sub(uiFS, "ui")
	files := http.FileServer(http.FS(ui))
	m.HandleFunc("GET /", s.admin(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && !strings.Contains(r.URL.Path, ".") {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	}))

	// Client-facing endpoints (Jellyfin / Plex / players): no auth.
	for _, prefix := range []string{"", "/p/{profile}"} {
		m.HandleFunc("GET "+prefix+"/discover.json", s.hdhrDiscover)
		m.HandleFunc("GET "+prefix+"/lineup.json", s.hdhrLineup)
		m.HandleFunc("GET "+prefix+"/lineup_status.json", s.hdhrLineupStatus)
		m.HandleFunc("POST "+prefix+"/lineup.post", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		m.HandleFunc("GET "+prefix+"/device.xml", s.hdhrDeviceXML)
		m.HandleFunc("GET "+prefix+"/playlist.m3u", s.m3u)
		m.HandleFunc("GET "+prefix+"/auto/{vnum}", s.streamAuto)
	}
	m.HandleFunc("GET /xmltv.xml", s.xmltvExport)
	m.HandleFunc("GET /stream/channel/{id}", s.streamChannel)
	m.HandleFunc("GET /stream/service/{id}", s.streamService)

	api := func(pattern string, h http.HandlerFunc) { m.HandleFunc(pattern, s.admin(h)) }
	api("GET /api/status", s.apiStatus)
	api("GET /api/signal", s.apiSignal)
	api("GET /api/system", s.apiSystem)
	api("GET /api/tuners", s.apiTuners)
	api("PUT /api/tuners/{key...}", s.apiPutTuner)
	api("POST /api/tuners-rediscover", s.apiRediscover)
	api("POST /api/tuners-drop/{key...}", s.apiDrop)
	api("GET /api/networks", s.apiNetworks)
	api("POST /api/networks", s.apiPutNetwork)
	api("PUT /api/networks/{id}", s.apiPutNetwork)
	api("DELETE /api/networks/{id}", s.apiDeleteNetwork)
	api("POST /api/networks/{id}/scan", s.apiScanNetwork)
	api("POST /api/networks/{id}/import", s.apiImportMuxes)
	api("GET /api/muxes", s.apiMuxes)
	api("POST /api/muxes", s.apiPutMux)
	api("PUT /api/muxes/{id}", s.apiPutMux)
	api("DELETE /api/muxes/{id}", s.apiDeleteMux)
	api("POST /api/muxes/{id}/scan", s.apiScanMux)
	api("GET /api/services", s.apiServices)
	api("PUT /api/services/{id}", s.apiPutService)
	api("POST /api/map", s.apiMap)
	api("GET /api/channels", s.apiChannels)
	api("POST /api/channels", s.apiPutChannel)
	api("PUT /api/channels/{id}", s.apiPutChannel)
	api("DELETE /api/channels/{id}", s.apiDeleteChannel)
	api("GET /api/profiles", s.apiProfiles)
	api("PUT /api/profiles/{id}", s.apiPutProfile)
	api("DELETE /api/profiles/{id}", s.apiDeleteProfile)
	api("GET /api/settings", s.apiSettings)
	api("PUT /api/settings", s.apiPutSettings)
	api("GET /api/epg/grid", s.apiEPGGrid)
	api("GET /api/epg/xmltv", s.apiXMLTVChannels)
	api("POST /api/epg/xmltv/refresh", s.apiXMLTVRefresh)
	api("POST /api/epg/automap", s.apiAutoMap)
	api("DELETE /api/subscriptions/{id}", s.apiKillSub)
	api("POST /api/align", s.alignStart)
	api("GET /api/align", s.alignPoll)
	api("DELETE /api/align", s.alignStop)
	api("GET /api/hardware", s.apiHardware)
	api("POST /api/hardware/install", s.apiHardwareInstall)
	api("GET /api/hardware/jobs/{id}", s.apiHardwareJob)
	api("GET /api/scanfiles", s.apiScanFiles)
	api("GET /api/scanfile", s.apiScanFile)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		log.Printf("web: encode: %v", err)
	}
}

func httpError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad JSON: %w", err)
	}
	return nil
}

var errNotFound = errors.New("not found")

// baseURL returns the URL clients should use to reach us.
func (s *Server) baseURL(r *http.Request) string {
	var base string
	s.st.View(func(st *store.State) { base = strings.TrimRight(st.Settings.BaseURL, "/") })
	if base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// channelsSorted returns enabled channels ordered by number.
func (s *Server) channelsSorted(includeDisabled bool) []*store.Channel {
	var out []*store.Channel
	s.st.View(func(st *store.State) {
		for _, c := range st.Channels {
			if c.Enabled || includeDisabled {
				cc := *c
				out = append(out, &cc)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Number != out[j].Number {
			return out[i].Number < out[j].Number
		}
		return out[i].Name < out[j].Name
	})
	return out
}
