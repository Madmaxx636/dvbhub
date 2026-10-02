// Package server serves the web UI and JSON API, the HDHomeRun emulation,
// M3U/XMLTV lineups and the live streams.
package server

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/guide"
	"dvbhub/internal/hardware"
	"dvbhub/internal/scan"
	"dvbhub/internal/transcode"
	"dvbhub/internal/tuners"
)

//go:embed ui
var uiFS embed.FS

// appJS is the UI script: ui/js/*.js joined in name order, served as one
// file so the Jellyfin plugin can load the same UI through Jellyfin.
var appJS = func() []byte {
	names, _ := fs.Glob(uiFS, "ui/js/*.js")
	sort.Strings(names)
	var b []byte
	for _, n := range names {
		data, _ := uiFS.ReadFile(n)
		b = append(b, "/* "+path.Base(n)+" */\n"...)
		b = append(b, data...)
		b = append(b, '\n')
	}
	return b
}()

// Deps are the parts of dvbhub the server exposes.
type Deps struct {
	Store    *config.Store
	Tuners   *tuners.Manager
	Scanner  *scan.Scanner
	Guide    *guide.Guide
	XMLTV    *guide.Importer
	Grabber  *guide.Grabber
	Hardware *hardware.Detector
	Encoders *transcode.Detector
	Logs     *LogBuffer
	Password string
	Version  string
}

type Server struct {
	Deps
	mux     *http.ServeMux
	align   aligner
	started time.Time
}

func New(d Deps) *Server {
	s := &Server{Deps: d, mux: http.NewServeMux(), align: aligner{byTuner: map[string]*alignSession{}}, started: time.Now()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// admin wraps handlers that need the admin password (when one is set).
func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Password != "" {
			_, pw, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(pw), []byte(s.Password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="dvbhub"`)
				httpError(w, http.StatusUnauthorized, errors.New("password required"))
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
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	m.HandleFunc("GET /app.js", s.admin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(appJS)
	}))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })

	// Client-facing endpoints (Jellyfin, Plex, players): no password.
	for _, prefix := range []string{"", "/p/{profile}"} {
		m.HandleFunc("GET "+prefix+"/discover.json", s.hdhrDiscover)
		m.HandleFunc("GET "+prefix+"/lineup.json", s.hdhrLineup)
		m.HandleFunc("GET "+prefix+"/lineup_status.json", s.hdhrLineupStatus)
		m.HandleFunc("POST "+prefix+"/lineup.post", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		m.HandleFunc("GET "+prefix+"/device.xml", s.hdhrDeviceXML)
		m.HandleFunc("GET "+prefix+"/auto/{vnum}", s.streamAuto)
		m.HandleFunc("GET "+prefix+"/playlist.m3u", s.m3u)
	}
	m.HandleFunc("GET /xmltv.xml", s.xmltvExport)
	m.HandleFunc("GET /stream/channel/{id}", s.streamChannel)
	m.HandleFunc("GET /stream/service/{id}", s.streamService)

	api := func(pattern string, h http.HandlerFunc) { m.HandleFunc(pattern, s.admin(h)) }
	api("GET /api/status", s.apiStatus)
	api("GET /api/signal", s.apiSignal)
	api("GET /api/logs", s.apiLogs)
	api("GET /api/jellyfin", s.apiJellyfin)

	api("GET /api/tuners", s.apiTuners)
	api("PUT /api/tuners/{key...}", s.apiPutTuner)
	api("POST /api/tuners-rediscover", s.apiRediscover)
	api("POST /api/tuners-drop/{key...}", s.apiDrop)
	api("DELETE /api/subscriptions/{id}", s.apiKillSub)

	api("GET /api/hardware", s.apiHardware)
	api("POST /api/hardware/install", s.apiHardwareInstall)
	api("GET /api/hardware/jobs/{id}", s.apiHardwareJob)

	api("GET /api/plans", s.apiPlans)
	api("GET /api/scanfiles", s.apiScanFiles)
	api("GET /api/scanfile", s.apiScanFile)
	api("POST /api/setup", s.apiSetup)
	api("GET /api/scan", s.apiScanStatus)
	api("POST /api/scan/cancel", s.apiScanCancel)

	api("GET /api/networks", s.apiNetworks)
	api("POST /api/networks", s.apiPutNetwork)
	api("PUT /api/networks/{id}", s.apiPutNetwork)
	api("DELETE /api/networks/{id}", s.apiDeleteNetwork)
	api("POST /api/networks/{id}/scan", s.apiScanNetwork)
	api("POST /api/networks/{id}/import", s.apiImportMuxes)
	api("POST /api/networks/{id}/plan", s.apiApplyPlan)

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
	api("POST /api/channels-bulk", s.apiChannelsBulk)

	api("GET /api/profiles", s.apiProfiles)
	api("PUT /api/profiles/{id}", s.apiPutProfile)
	api("DELETE /api/profiles/{id}", s.apiDeleteProfile)
	api("GET /api/transcode", s.apiTranscode)
	api("POST /api/transcode/probe", s.apiTranscodeProbe)

	api("GET /api/settings", s.apiSettings)
	api("PUT /api/settings", s.apiPutSettings)

	api("GET /api/guide", s.apiGuide)
	api("GET /api/guide/search", s.apiGuideSearch)
	api("GET /api/guide/status", s.apiGuideStatus)
	api("POST /api/guide/grab", s.apiGuideGrab)
	api("DELETE /api/guide", s.apiGuideClear)
	api("GET /api/xmltv/channels", s.apiXMLTVChannels)
	api("POST /api/xmltv/refresh", s.apiXMLTVRefresh)
	api("POST /api/xmltv/automap", s.apiXMLTVAutoMap)

	api("POST /api/align", s.alignStart)
	api("GET /api/align", s.alignPoll)
	api("DELETE /api/align", s.alignStop)
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	// A nil slice would encode as null, which the UI can't iterate.
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.IsNil() {
		v = []any{}
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("server: encode: %v", err)
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

// readBody reads the request body once so it can be merged into an object.
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(http.MaxBytesReader(nil, r.Body, 8<<20))
}

// merge applies a JSON body onto v: fields the client sent replace v's,
// fields it left out keep their values. So a level that shows fewer
// settings never resets the ones it doesn't show.
func merge(body []byte, v any) error {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("bad JSON: %w", err)
	}
	return nil
}

// cloneMerge returns a deep copy of old with the JSON body merged in, so a
// rejected edit never touches the stored object.
func cloneMerge[T any](old *T, body []byte) (*T, error) {
	b, err := json.Marshal(old)
	if err != nil {
		return nil, err
	}
	n := new(T)
	if err := json.Unmarshal(b, n); err != nil {
		return nil, err
	}
	if err := merge(body, n); err != nil {
		return nil, err
	}
	return n, nil
}

var errNotFound = errors.New("not found")

func ok(w http.ResponseWriter) { writeJSON(w, map[string]bool{"ok": true}) }

// baseURL is the URL clients use to reach dvbhub.
func (s *Server) baseURL(r *http.Request) string {
	var base string
	s.Store.View(func(st *config.State) { base = strings.TrimRight(st.Settings.BaseURL, "/") })
	if base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// channelsSorted returns copies of the channels, ordered by number.
func (s *Server) channelsSorted(includeDisabled bool) []*config.Channel {
	out := []*config.Channel{}
	s.Store.View(func(st *config.State) {
		for _, c := range st.Channels {
			if c.Enabled || includeDisabled {
				cc := *c
				cc.Services = append([]string(nil), c.Services...)
				out = append(out, &cc)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Number != out[j].Number {
			return out[i].Number < out[j].Number
		}
		if out[i].Minor != out[j].Minor {
			return out[i].Minor < out[j].Minor
		}
		return out[i].Name < out[j].Name
	})
	return out
}
