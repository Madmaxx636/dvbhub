package web

import (
	"errors"
	"fmt"
	"hash/crc32"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dvbhub/internal/epg"
	"dvbhub/internal/store"
	"dvbhub/internal/stream"
	"dvbhub/internal/tuner"
)

// ---- HDHomeRun emulation ----

// lineupProfile returns the profile id for a request: the /p/{profile}
// prefix, or the default HDHomeRun profile.
func (s *Server) lineupProfile(r *http.Request) (id, prefix string) {
	if p := r.PathValue("profile"); p != "" {
		return p, "/p/" + url.PathEscape(p)
	}
	s.st.View(func(st *store.State) { id = st.Settings.HDHRProfile })
	return id, ""
}

// deviceID derives a distinct, checksum-valid device id per profile so
// Jellyfin/Plex treat each profile lineup as a separate tuner device.
func deviceID(base, profile, defProfile string) string {
	if profile == "" || profile == defProfile {
		return base
	}
	v, _ := strconv.ParseUint(base, 16, 32)
	id := uint32(v) ^ (crc32.ChecksumIEEE([]byte(profile)) &^ 0xf)
	id &^= 0xf
	lut := [16]uint32{0xa, 0x5, 0xf, 0x6, 0x7, 0xc, 0x1, 0xb, 0x9, 0x2, 0x8, 0xd, 0x4, 0x3, 0xe, 0x0}
	c := lut[id>>28&0xf] ^ id>>24&0xf ^ lut[id>>20&0xf] ^ id>>16&0xf ^ lut[id>>12&0xf] ^ id>>8&0xf ^ lut[id>>4&0xf]
	return fmt.Sprintf("%08X", id|c)
}

func (s *Server) deviceInfo(r *http.Request) map[string]any {
	prof, prefix := s.lineupProfile(r)
	var name, id, def, profName string
	s.st.View(func(st *store.State) {
		name, id, def = st.Settings.ServerName, st.Settings.DeviceID, st.Settings.HDHRProfile
		if p, ok := st.Profiles[prof]; ok {
			profName = p.Name
		}
	})
	if prefix != "" && profName != "" {
		name += " (" + profName + ")"
	}
	base := s.baseURL(r) + prefix
	return map[string]any{
		"FriendlyName":    name,
		"Manufacturer":    "Silicondust",
		"ManufacturerURL": "https://github.com/Madmaxx636/dvbhub",
		"ModelNumber":     "HDTC-2US",
		"FirmwareName":    "hdhomeruntc_atsc",
		"FirmwareVersion": "20200101",
		"DeviceID":        deviceID(id, prof, def),
		"DeviceAuth":      "dvbhub",
		"BaseURL":         base,
		"LineupURL":       base + "/lineup.json",
		"TunerCount":      max(s.tm.TunerCount(), 1),
	}
}

func (s *Server) hdhrDiscover(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.deviceInfo(r)) }

func (s *Server) hdhrLineupStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ScanInProgress": 0, "ScanPossible": 1, "Source": "Antenna", "SourceList": []string{"Antenna", "Cable"}})
}

func (s *Server) hdhrLineup(w http.ResponseWriter, r *http.Request) {
	_, prefix := s.lineupProfile(r)
	base := s.baseURL(r) + prefix
	type entry struct {
		GuideNumber string
		GuideName   string
		URL         string
		HD          int    `json:",omitempty"`
		VideoCodec  string `json:",omitempty"`
		AudioCodec  string `json:",omitempty"`
	}
	out := []entry{}
	for _, c := range s.channelsSorted(false) {
		e := entry{GuideNumber: epg.GuideNumber(c), GuideName: c.Name, URL: base + "/auto/v" + epg.GuideNumber(c)}
		s.st.View(func(st *store.State) {
			if len(c.Services) > 0 {
				if svc, ok := st.Services[c.Services[0]]; ok {
					switch svc.Type {
					case 0x11, 0x19, 0x1f, 0x20:
						e.HD = 1
					}
					for _, es := range svc.Streams {
						switch es.Kind {
						case "H264", "HEVC", "MPEG2VIDEO":
							if e.VideoCodec == "" {
								e.VideoCodec = map[string]string{"H264": "H264", "HEVC": "HEVC", "MPEG2VIDEO": "MPEG2"}[es.Kind]
							}
						case "AAC", "AAC-LATM", "AC3", "EAC3", "MPEG2AUDIO":
							if e.AudioCodec == "" {
								e.AudioCodec = map[string]string{"AAC": "AAC", "AAC-LATM": "AAC", "AC3": "AC3", "EAC3": "EAC3", "MPEG2AUDIO": "MPEG"}[es.Kind]
							}
						}
					}
				}
			}
		})
		out = append(out, e)
	}
	writeJSON(w, out)
}

func (s *Server) hdhrDeviceXML(w http.ResponseWriter, r *http.Request) {
	info := s.deviceInfo(r)
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <URLBase>%s</URLBase>
  <device>
    <deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>
    <friendlyName>%s</friendlyName>
    <manufacturer>Silicondust</manufacturer>
    <modelName>HDTC-2US</modelName>
    <modelNumber>HDTC-2US</modelNumber>
    <serialNumber>%s</serialNumber>
    <UDN>uuid:%s</UDN>
  </device>
</root>
`, xmlText(info["BaseURL"].(string)), xmlText(info["FriendlyName"].(string)), info["DeviceID"], upnpUUID(info["DeviceID"].(string)))
}

func upnpUUID(id string) string {
	return "2f7a3e90-8b8c-4c5d-9e1f-" + strings.ToLower(fmt.Sprintf("%012s", id))
}

func xmlText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// ---- M3U / XMLTV ----

func (s *Server) m3u(w http.ResponseWriter, r *http.Request) {
	prof, _ := s.lineupProfile(r)
	base := s.baseURL(r)
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	fmt.Fprintf(w, "#EXTM3U url-tvg=\"%s/xmltv.xml\" x-tvg-url=\"%s/xmltv.xml\"\n", base, base)
	for _, c := range s.channelsSorted(false) {
		group := "TV"
		if c.Radio {
			group = "Radio"
		}
		num := epg.GuideNumber(c)
		fmt.Fprintf(w, "#EXTINF:-1 tvg-id=\"%s\" tvg-chno=\"%s\" tvg-name=%q tvg-logo=%q group-title=\"%s\",%s\n",
			num, num, c.Name, c.Icon, group, c.Name)
		u := base + "/stream/channel/" + url.PathEscape(c.ID)
		if prof != "" {
			u += "?profile=" + url.QueryEscape(prof)
		}
		fmt.Fprintln(w, u)
	}
}

func (s *Server) xmltvExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	s.guide.WriteXMLTV(w, s.channelsSorted(false), func(c *store.Channel) string { return c.Icon })
}

// ---- streaming ----

func (s *Server) streamAuto(w http.ResponseWriter, r *http.Request) {
	num := strings.TrimPrefix(r.PathValue("vnum"), "v")
	for _, c := range s.channelsSorted(false) {
		if epg.GuideNumber(c) == num {
			prof, _ := s.lineupProfile(r)
			s.serveChannel(w, r, c, prof)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) streamChannel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var ch *store.Channel
	s.st.View(func(st *store.State) {
		if c, ok := st.Channels[id]; ok {
			cc := *c
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
	s.st.View(func(st *store.State) {
		if svc, ok := st.Services[id]; ok {
			name = svc.Name
		}
	})
	if name == "" {
		http.NotFound(w, r)
		return
	}
	s.serveChannel(w, r, &store.Channel{ID: "svc:" + id, Name: name, Services: []string{id}, Enabled: true}, "")
}

func clientName(r *http.Request) string {
	ua := r.UserAgent()
	switch {
	case strings.Contains(ua, "Jellyfin"):
		ua = "Jellyfin"
	case strings.Contains(ua, "Lavf"):
		ua = "ffmpeg/" + ua
	case strings.Contains(ua, "Plex"):
		ua = "Plex"
	}
	if len(ua) > 40 {
		ua = ua[:40]
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host + " " + ua
}

func (s *Server) serveChannel(w http.ResponseWriter, r *http.Request, ch *store.Channel, lineupProfile string) {
	profID := r.URL.Query().Get("profile")
	if profID == "" {
		profID = ch.Profile
	}
	if profID == "" {
		profID = lineupProfile
	}
	var prof *store.Profile
	var ffmpeg string
	var services []string
	s.st.View(func(st *store.State) {
		if profID == "" {
			profID = st.Settings.HDHRProfile
		}
		if p, ok := st.Profiles[profID]; ok {
			pc := *p
			prof = &pc
		}
		ffmpeg = st.Settings.FFmpeg
		for _, id := range ch.Services {
			if svc, ok := st.Services[id]; ok && svc.Enabled {
				services = append(services, id)
			}
		}
	})
	if profID != "" && prof == nil {
		http.Error(w, "unknown profile "+profID, http.StatusBadRequest)
		return
	}
	if len(services) == 0 {
		http.Error(w, "channel has no enabled services", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	profName := "Passthrough"
	if prof != nil {
		profName = prof.Name
	}
	sub, err := s.tm.Subscribe(tuner.Request{Services: services, Weight: tuner.WeightLive, Name: ch.Name,
		Client: clientName(r), Profile: profName})
	if err != nil {
		code := http.StatusServiceUnavailable
		if !errors.Is(err, tuner.ErrNoTuner) {
			code = http.StatusInternalServerError
		}
		w.Header().Del("Content-Type")
		http.Error(w, err.Error(), code)
		log.Printf("stream: %s for %s refused: %v", ch.Name, clientName(r), err)
		return
	}
	defer sub.Close()
	log.Printf("stream: %s -> %s [%s]", ch.Name, clientName(r), profName)
	start := time.Now()
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}
	flush()
	err = stream.Serve(r.Context(), sub, prof, ffmpeg, w, flush)
	log.Printf("stream: %s -> %s ended after %s (%v)", ch.Name, clientName(r), time.Since(start).Round(time.Second), errOr(err, "client closed"))
}

func errOr(err error, def string) string {
	if err != nil {
		return err.Error()
	}
	return def
}

// ---- SSDP ----

// RunSSDP answers UPnP M-SEARCH queries so Plex/Jellyfin can discover us.
func RunSSDP(st *store.Store, port int) {
	addr, _ := net.ResolveUDPAddr("udp4", "239.255.255.250:1900")
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("ssdp: disabled: %v", err)
		return
	}
	log.Printf("ssdp: listening for discovery on 239.255.255.250:1900")
	buf := make([]byte, 2048)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("ssdp: %v", err)
			return
		}
		msg := string(buf[:n])
		if !strings.HasPrefix(msg, "M-SEARCH") {
			continue
		}
		st0 := ""
		for _, line := range strings.Split(msg, "\r\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "ST") {
				st0 = strings.TrimSpace(v)
			}
		}
		if st0 != "ssdp:all" && st0 != "upnp:rootdevice" && st0 != "urn:schemas-upnp-org:device:MediaServer:1" {
			continue
		}
		var id, base string
		st.View(func(s *store.State) { id, base = s.Settings.DeviceID, strings.TrimRight(s.Settings.BaseURL, "/") })
		if base == "" {
			ip := localIPFor(src.IP)
			if ip == "" {
				continue
			}
			base = fmt.Sprintf("http://%s:%d", ip, port)
		}
		resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\nLOCATION: %s/device.xml\r\nSERVER: Linux/1.0 UPnP/1.0 dvbhub/1.0\r\nST: %s\r\nUSN: uuid:%s::%s\r\n\r\n",
			base, st0, upnpUUID(id), st0)
		if c, err := net.DialUDP("udp4", nil, src); err == nil {
			c.Write([]byte(resp))
			c.Close()
		}
	}
}

func localIPFor(remote net.IP) string {
	c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: remote, Port: 9})
	if err != nil {
		return ""
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}
