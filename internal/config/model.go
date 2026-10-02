// Package config holds dvbhub's persistent state: networks, muxes, services,
// channels, tuners, stream profiles and settings. There is one set of
// settings; the UI's Simple/Advanced/Pro levels only change which of them are
// shown, never what they are.
package config

import (
	"strconv"
	"time"

	"dvbhub/internal/dvb"
)

// Network is a broadcast source: an antenna region, a cable system or a satellite.
type Network struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`           // atsc, cable, dvbt, dvbc, dvbs, virtual
	Plan          string `json:"plan,omitempty"` // built-in channel plan used to create muxes
	NetworkID     uint16 `json:"networkId,omitempty"`
	DiscoverMuxes bool   `json:"discoverMuxes"` // DVB: add muxes announced in the NIT
}

// ScanState is the result of the last scan of a mux.
type ScanState struct {
	Status string    `json:"status"` // new, queued, scanning, ok, nosignal, fail
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
}

// Mux is one RF channel (multiplex) carrying several services.
type Mux struct {
	ID        string          `json:"id"`
	NetworkID string          `json:"networkId"`
	Label     string          `json:"label"`
	Tuning    dvb.Tuning      `json:"tuning"`
	File      string          `json:"file,omitempty"` // virtual networks: transport stream file
	TSID      uint16          `json:"tsid"`
	ONID      uint16          `json:"onid"`
	Enabled   bool            `json:"enabled"`
	Scan      ScanState       `json:"scan"`
	Signal    *dvb.SignalSnap `json:"signal,omitempty"`
}

// Stream is one elementary stream (video, audio, subtitles) of a service.
type Stream struct {
	PID        uint16 `json:"pid"`
	StreamType byte   `json:"streamType"`
	Kind       string `json:"kind"` // H264, HEVC, MPEG2VIDEO, AC3, AAC, ...
	Lang       string `json:"lang,omitempty"`
}

// Service is a TV or radio programme found on a mux.
type Service struct {
	ID        string    `json:"id"`
	MuxID     string    `json:"muxId"`
	SID       uint16    `json:"sid"` // MPEG program number
	Name      string    `json:"name"`
	Provider  string    `json:"provider,omitempty"`
	Type      byte      `json:"type"`
	Kind      string    `json:"kind"`            // tv, radio, other
	Major     int       `json:"major,omitempty"` // ATSC virtual channel or DVB logical channel number
	Minor     int       `json:"minor,omitempty"` // ATSC minor (the .1 in 7.1)
	SourceID  uint16    `json:"sourceId,omitempty"`
	PMTPID    uint16    `json:"pmtPid"`
	PCRPID    uint16    `json:"pcrPid"`
	Streams   []Stream  `json:"streams"`
	Scrambled bool      `json:"scrambled"`
	Enabled   bool      `json:"enabled"`
	LastSeen  time.Time `json:"lastSeen"`
}

// VideoCodec returns the first video stream's codec, or "".
func (s *Service) VideoCodec() string {
	for _, st := range s.Streams {
		switch st.Kind {
		case "MPEG2VIDEO", "H264", "HEVC":
			return st.Kind
		}
	}
	return ""
}

// Channel is what viewers see: a number, a name and one or more services
// (later ones are backups used when the first has no signal).
type Channel struct {
	ID       string   `json:"id"`
	Number   int      `json:"number"`
	Minor    int      `json:"minor,omitempty"`
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	Services []string `json:"services"`
	Icon     string   `json:"icon,omitempty"`
	GuideID  string   `json:"guideId,omitempty"` // XMLTV channel id; empty = over-the-air guide
	Profile  string   `json:"profile,omitempty"` // stream profile override
	Radio    bool     `json:"radio"`
}

// GuideNumber is the channel number as shown to viewers ("7" or "7.1").
func (c *Channel) GuideNumber() string {
	if c.Minor > 0 {
		return itoa(c.Number) + "." + itoa(c.Minor)
	}
	return itoa(c.Number)
}

// Tuner is the configuration of one tuner (hardware frontend or virtual).
type Tuner struct {
	Key         string                  `json:"key"` // adapter0/frontend0 or virtual0
	Name        string                  `json:"name"`
	Enabled     bool                    `json:"enabled"`
	Networks    []string                `json:"networks"`    // empty = any compatible network
	Priority    int                     `json:"priority"`    // higher is used first
	TuneTimeout int                     `json:"tuneTimeout"` // seconds to wait for lock
	Hold        bool                    `json:"hold"`        // keep open so other programs can't use it
	Sat         map[string]dvb.SatInput `json:"sat,omitempty"`
}

// Profile controls how a channel is delivered: passed through untouched, or
// transcoded with ffmpeg (GPU or CPU) at a chosen size and bitrate.
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Mode: passthrough = original stream; transcode = always re-encode;
	// mpeg2 = re-encode only MPEG-2 video (US over-the-air), pass the rest.
	Mode        string `json:"mode"`
	Encoder     string `json:"encoder"`     // auto, nvenc, qsv, vaapi, cpu
	Codec       string `json:"codec"`       // h264, hevc, av1
	Height      int    `json:"height"`      // 0 = keep
	FPS         int    `json:"fps"`         // 0 = keep
	Deinterlace string `json:"deinterlace"` // auto, on, off
	RateControl string `json:"rateControl"` // vbr, cbr, cq
	BitrateK    int    `json:"bitrate"`     // video kbit/s
	MaxrateK    int    `json:"maxrate"`     // peak kbit/s (vbr)
	BufsizeK    int    `json:"bufsize"`     // VBV buffer kbit (0 = 2x bitrate)
	Quality     int    `json:"quality"`     // cq / crf value
	Preset      string `json:"preset"`      // fast, balanced, quality
	GOP         int    `json:"gop"`         // keyframe interval in frames (0 = 2 s)
	AudioCodec  string `json:"audioCodec"`  // copy, aac, ac3, opus
	AudioK      int    `json:"audioBitrate"`
	AudioCh     int    `json:"audioChannels"` // 0 = keep
	AudioTracks string `json:"audioTracks"`   // all, first
	Subtitles   string `json:"subtitles"`     // keep, drop
	GPU         int    `json:"gpu"`           // GPU index for nvenc / render node for vaapi
	Extra       string `json:"extra,omitempty"`
}

// Passthrough reports whether the profile never runs ffmpeg.
func (p *Profile) Passthrough() bool { return p == nil || p.Mode == "" || p.Mode == "passthrough" }

type XMLTVSource struct {
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

// Settings are global options.
type Settings struct {
	ServerName     string        `json:"serverName"`
	BaseURL        string        `json:"baseUrl"`
	DeviceID       string        `json:"deviceId"`
	SSDP           bool          `json:"ssdp"`
	DefaultProfile string        `json:"defaultProfile"`
	FFmpeg         string        `json:"ffmpeg"`
	HWAccel        string        `json:"hwAccel"` // default encoder for "auto": auto, nvenc, qsv, vaapi, cpu
	EIT            bool          `json:"eit"`     // over-the-air guide
	GuideGrabHours int           `json:"guideGrabHours"`
	GuideDays      int           `json:"guideDays"`
	XMLTV          []XMLTVSource `json:"xmltv"`
	XMLTVHours     int           `json:"xmltvHours"`
	FailoverSecs   int           `json:"failoverSecs"`  // switch to a backup service after this long without signal
	MaxOutageSecs  int           `json:"maxOutageSecs"` // give up and free the tuner
	LingerSecs     int           `json:"lingerSecs"`    // keep a tuner tuned after the last viewer leaves
	HotplugSecs    int           `json:"hotplugSecs"`   // how often to look for new/removed tuners
	VirtualTuners  int           `json:"virtualTuners"` // test tuners that play files
	UILevel        string        `json:"uiLevel"`       // default level for new browsers: simple, advanced, pro
}

// State is everything that is persisted.
type State struct {
	Networks map[string]*Network `json:"networks"`
	Muxes    map[string]*Mux     `json:"muxes"`
	Services map[string]*Service `json:"services"`
	Channels map[string]*Channel `json:"channels"`
	Tuners   map[string]*Tuner   `json:"tuners"`
	Profiles map[string]*Profile `json:"profiles"`
	Settings Settings            `json:"settings"`
}

// DefaultSettings returns the settings of a fresh install.
func DefaultSettings() Settings {
	return Settings{ServerName: "dvbhub", SSDP: true, DefaultProfile: "original", FFmpeg: "ffmpeg", HWAccel: "auto",
		EIT: true, GuideGrabHours: 6, GuideDays: 7, XMLTVHours: 12, FailoverSecs: 8, MaxOutageSecs: 60,
		LingerSecs: 4, HotplugSecs: 10, UILevel: "simple"}
}

// DefaultProfiles returns the built-in stream profiles.
func DefaultProfiles() map[string]*Profile {
	base := func(id, name string) *Profile {
		return &Profile{ID: id, Name: name, Mode: "transcode", Encoder: "auto", Codec: "h264", Deinterlace: "auto",
			RateControl: "vbr", Preset: "balanced", AudioCodec: "aac", AudioK: 192, AudioCh: 2, AudioTracks: "all", Subtitles: "drop"}
	}
	hd := base("hd", "HD 1080p (8 Mbit/s)")
	hd.Height, hd.BitrateK, hd.MaxrateK = 1080, 8000, 12000
	sd := base("mobile", "Mobile 720p (3 Mbit/s)")
	sd.Height, sd.BitrateK, sd.MaxrateK, sd.AudioK = 720, 3000, 4000, 128
	low := base("low", "Low 480p (1.5 Mbit/s)")
	low.Height, low.BitrateK, low.MaxrateK, low.AudioK = 480, 1500, 2000, 96
	mp2 := base("mpeg2", "Convert MPEG-2 only (keeps HD)")
	mp2.Mode, mp2.BitrateK, mp2.MaxrateK = "mpeg2", 8000, 12000
	return map[string]*Profile{
		"original": {ID: "original", Name: "Original (passthrough)", Mode: "passthrough", Encoder: "auto", Codec: "h264",
			Deinterlace: "auto", RateControl: "vbr", Preset: "balanced", AudioCodec: "copy", AudioTracks: "all", Subtitles: "keep"},
		"mpeg2": mp2, "hd": hd, "mobile": sd, "low": low,
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
