package server

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"dvbhub/internal/config"
)

// ---- HDHomeRun emulation (what Jellyfin and Plex talk to) ----

// lineupProfile returns the stream profile of a request (the /p/{profile}
// prefix or the default profile) and the URL prefix that selects it.
func (s *Server) lineupProfile(r *http.Request) (id, prefix string) {
	if p := r.PathValue("profile"); p != "" {
		return p, "/p/" + url.PathEscape(p)
	}
	s.Store.View(func(st *config.State) { id = st.Settings.DefaultProfile })
	return id, ""
}

// deviceID derives a distinct, checksum-valid device id per profile, so
// Jellyfin and Plex treat each profile's lineup as a separate tuner device.
func deviceID(base, profile, defProfile string) string {
	if profile == "" || profile == defProfile {
		return base
	}
	v, _ := strconv.ParseUint(base, 16, 32)
	return config.HDHRDeviceID(uint32(v) ^ crc32.ChecksumIEEE([]byte(profile)))
}

type deviceInfo struct {
	FriendlyName    string
	Manufacturer    string
	ManufacturerURL string
	ModelNumber     string
	FirmwareName    string
	FirmwareVersion string
	DeviceID        string
	DeviceAuth      string
	BaseURL         string
	LineupURL       string
	TunerCount      int
}

func (s *Server) deviceInfo(r *http.Request) deviceInfo {
	prof, prefix := s.lineupProfile(r)
	var name, id, def, profName string
	s.Store.View(func(st *config.State) {
		name, id, def = st.Settings.ServerName, st.Settings.DeviceID, st.Settings.DefaultProfile
		if p, ok := st.Profiles[prof]; ok {
			profName = p.Name
		}
	})
	if prefix != "" && profName != "" {
		name += " (" + profName + ")"
	}
	base := s.baseURL(r) + prefix
	return deviceInfo{FriendlyName: name, Manufacturer: "Silicondust", ManufacturerURL: "https://github.com/Madmaxx636/dvbhub",
		ModelNumber: "HDTC-2US", FirmwareName: "hdhomeruntc_atsc", FirmwareVersion: "20250101",
		DeviceID: deviceID(id, prof, def), DeviceAuth: "dvbhub", BaseURL: base, LineupURL: base + "/lineup.json",
		TunerCount: max(s.Tuners.TunerCount(), 1)}
}

func (s *Server) hdhrDiscover(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.deviceInfo(r)) }

func (s *Server) hdhrLineupStatus(w http.ResponseWriter, r *http.Request) {
	scanning := 0
	if s.Scanner.Busy() {
		scanning = 1
	}
	writeJSON(w, map[string]any{"ScanInProgress": scanning, "ScanPossible": 1, "Source": "Antenna", "SourceList": []string{"Antenna", "Cable"}})
}

var codecNames = map[string]string{"H264": "H264", "HEVC": "HEVC", "MPEG2VIDEO": "MPEG2",
	"AAC": "AAC", "AAC-LATM": "AAC", "AC3": "AC3", "EAC3": "EAC3", "MPEG2AUDIO": "MPEG"}

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
		e := entry{GuideNumber: c.GuideNumber(), GuideName: c.Name, URL: base + "/auto/v" + c.GuideNumber()}
		s.Store.View(func(st *config.State) {
			if len(c.Services) == 0 {
				return
			}
			svc, ok := st.Services[c.Services[0]]
			if !ok {
				return
			}
			switch svc.Type {
			case 0x11, 0x19, 0x1f, 0x20:
				e.HD = 1
			}
			for _, es := range svc.Streams {
				n := codecNames[es.Kind]
				switch es.Kind {
				case "H264", "HEVC", "MPEG2VIDEO":
					if e.VideoCodec == "" {
						e.VideoCodec = n
					}
				case "AAC", "AAC-LATM", "AC3", "EAC3", "MPEG2AUDIO":
					if e.AudioCodec == "" {
						e.AudioCodec = n
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
`, xmlText(info.BaseURL), xmlText(info.FriendlyName), info.DeviceID, upnpUUID(info.DeviceID))
}

func upnpUUID(id string) string {
	return "2f7a3e90-8b8c-4c5d-9e1f-" + strings.ToLower(fmt.Sprintf("%012s", id))
}

func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// ---- M3U / XMLTV ----

func (s *Server) m3u(w http.ResponseWriter, r *http.Request) {
	prof, _ := s.lineupProfile(r)
	if q := r.URL.Query().Get("profile"); q != "" {
		prof = q
	}
	base := s.baseURL(r)
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	fmt.Fprintf(w, "#EXTM3U url-tvg=\"%s/xmltv.xml\" x-tvg-url=\"%s/xmltv.xml\"\n", base, base)
	for _, c := range s.channelsSorted(false) {
		group := "TV"
		if c.Radio {
			group = "Radio"
		}
		num := c.GuideNumber()
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
	days := 7
	s.Store.View(func(st *config.State) { days = st.Settings.GuideDays })
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	s.Guide.WriteXMLTV(w, s.channelsSorted(false), days)
}

// ---- network discovery ----

// RunDiscovery answers SSDP (UPnP) and HDHomeRun UDP discovery so Jellyfin
// and Plex can find dvbhub on the local network. port is the HTTP port.
func RunDiscovery(st *config.Store, tunerCount func() int, port int) {
	go runSSDP(st, port)
	go runHDHRDiscovery(st, tunerCount, port)
}

func discoveryBase(st *config.Store, remote net.IP, port int) (id, base string) {
	st.View(func(s *config.State) { id, base = s.Settings.DeviceID, strings.TrimRight(s.Settings.BaseURL, "/") })
	if base == "" {
		ip := localIPFor(remote)
		if ip == "" {
			return id, ""
		}
		base = fmt.Sprintf("http://%s:%d", ip, port)
	}
	return id, base
}

func runSSDP(st *config.Store, port int) {
	addr, _ := net.ResolveUDPAddr("udp4", "239.255.255.250:1900")
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("discovery: SSDP off: %v", err)
		return
	}
	buf := make([]byte, 2048)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("discovery: SSDP: %v", err)
			return
		}
		msg := string(buf[:n])
		if !strings.HasPrefix(msg, "M-SEARCH") {
			continue
		}
		target := ""
		for _, line := range strings.Split(msg, "\r\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "ST") {
				target = strings.TrimSpace(v)
			}
		}
		if target != "ssdp:all" && target != "upnp:rootdevice" && target != "urn:schemas-upnp-org:device:MediaServer:1" {
			continue
		}
		id, base := discoveryBase(st, src.IP, port)
		if base == "" {
			continue
		}
		resp := fmt.Sprintf("HTTP/1.1 200 OK\r\nCACHE-CONTROL: max-age=1800\r\nEXT:\r\nLOCATION: %s/device.xml\r\nSERVER: Linux/1.0 UPnP/1.0 dvbhub/1.0\r\nST: %s\r\nUSN: uuid:%s::%s\r\n\r\n",
			base, target, upnpUUID(id), target)
		if c, err := net.DialUDP("udp4", nil, src); err == nil {
			c.Write([]byte(resp))
			c.Close()
		}
	}
}

// HDHomeRun discovery protocol (UDP 65001): a discover request (type 2)
// is answered with a reply (type 3) carrying device type, id, tuner count
// and URLs as TLVs, followed by a little-endian CRC-32.
const (
	hdhrDiscoverReq   = 0x0002
	hdhrDiscoverReply = 0x0003
	tagDeviceType     = 0x01
	tagDeviceID       = 0x02
	tagTunerCount     = 0x10
	tagLineupURL      = 0x27
	tagBaseURL        = 0x2a
	tagDeviceAuth     = 0x2b
)

func hdhrPacket(typ uint16, payload []byte) []byte {
	p := make([]byte, 4, 8+len(payload))
	binary.BigEndian.PutUint16(p, typ)
	binary.BigEndian.PutUint16(p[2:], uint16(len(payload)))
	p = append(p, payload...)
	return binary.LittleEndian.AppendUint32(p, crc32.ChecksumIEEE(p))
}

func tlv(tag byte, v []byte) []byte {
	out := []byte{tag}
	if len(v) < 128 {
		out = append(out, byte(len(v)))
	} else {
		out = append(out, byte(len(v)&0x7f)|0x80, byte(len(v)>>7))
	}
	return append(out, v...)
}

func runHDHRDiscovery(st *config.Store, tunerCount func() int, port int) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 65001})
	if err != nil {
		log.Printf("discovery: HDHomeRun UDP off: %v", err)
		return
	}
	buf := make([]byte, 1500)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("discovery: HDHomeRun UDP: %v", err)
			return
		}
		if n < 8 || binary.BigEndian.Uint16(buf) != hdhrDiscoverReq {
			continue
		}
		if l := int(binary.BigEndian.Uint16(buf[2:])); 4+l+4 > n || crc32.ChecksumIEEE(buf[:4+l]) != binary.LittleEndian.Uint32(buf[4+l:]) {
			continue
		}
		id, base := discoveryBase(st, src.IP, port)
		if base == "" {
			continue
		}
		idv, _ := strconv.ParseUint(id, 16, 32)
		var payload []byte
		payload = append(payload, tlv(tagDeviceType, binary.BigEndian.AppendUint32(nil, 1))...)
		payload = append(payload, tlv(tagDeviceID, binary.BigEndian.AppendUint32(nil, uint32(idv)))...)
		payload = append(payload, tlv(tagTunerCount, []byte{byte(max(tunerCount(), 1))})...)
		payload = append(payload, tlv(tagBaseURL, []byte(base))...)
		payload = append(payload, tlv(tagLineupURL, []byte(base+"/lineup.json"))...)
		payload = append(payload, tlv(tagDeviceAuth, []byte("dvbhub"))...)
		conn.WriteToUDP(hdhrPacket(hdhrDiscoverReply, payload), src)
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
