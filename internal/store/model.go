// Package store holds the persistent configuration: networks, muxes,
// services, channels, tuner settings, transcode profiles and global settings.
package store

import "time"

// Tuning describes how to tune a multiplex. Frequencies are always kHz.
type Tuning struct {
	DeliverySystem   string `json:"delsys"` // DVB-T, DVB-T2, DVB-C, DVB-S, DVB-S2
	FrequencyKHz     uint32 `json:"frequency"`
	BandwidthHz      uint32 `json:"bandwidth,omitempty"`
	SymbolRate       uint32 `json:"symbolRate,omitempty"`
	Modulation       string `json:"modulation,omitempty"`
	FEC              string `json:"fec,omitempty"`
	CodeRateLP       string `json:"fecLP,omitempty"`
	TransmissionMode string `json:"transmissionMode,omitempty"`
	GuardInterval    string `json:"guardInterval,omitempty"`
	Hierarchy        string `json:"hierarchy,omitempty"`
	Polarization     string `json:"polarization,omitempty"` // H, V, L, R
	Rolloff          string `json:"rolloff,omitempty"`
	Pilot            string `json:"pilot,omitempty"`
	Inversion        string `json:"inversion,omitempty"`
	StreamID         int    `json:"streamId"` // PLP id / ISI, -1 for none
}

type Network struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"` // dvbt, dvbc, dvbs, virtual
	NetworkID     uint16 `json:"networkId,omitempty"`
	DiscoverMuxes bool   `json:"discoverMuxes"` // add muxes found in the NIT
	Orbital       string `json:"orbital,omitempty"`
}

type Mux struct {
	ID         string    `json:"id"`
	NetworkID  string    `json:"networkId"`
	Tuning     Tuning    `json:"tuning"`
	File       string    `json:"file,omitempty"` // virtual networks: TS file to play
	TSID       uint16    `json:"tsid"`
	ONID       uint16    `json:"onid"`
	Enabled    bool      `json:"enabled"`
	ScanStatus string    `json:"scanStatus"` // new, pending, scanning, ok, fail
	ScanError  string    `json:"scanError,omitempty"`
	LastScan   time.Time `json:"lastScan"`
}

type Stream struct {
	PID        uint16 `json:"pid"`
	StreamType byte   `json:"streamType"`
	Kind       string `json:"kind"`
	Lang       string `json:"lang,omitempty"`
}

type Service struct {
	ID        string    `json:"id"`
	MuxID     string    `json:"muxId"`
	SID       uint16    `json:"sid"`
	Name      string    `json:"name"`
	Provider  string    `json:"provider"`
	Type      byte      `json:"type"`
	Kind      string    `json:"kind"`            // tv, radio, other
	LCN       int       `json:"lcn,omitempty"`   // DVB LCN, or ATSC major channel
	Minor     int       `json:"minor,omitempty"` // ATSC minor channel (the 1 in 3.1)
	PMTPID    uint16    `json:"pmtPid"`
	PCRPID    uint16    `json:"pcrPid"`
	Streams   []Stream  `json:"streams"`
	Scrambled bool      `json:"scrambled"`
	Enabled   bool      `json:"enabled"`
	LastSeen  time.Time `json:"lastSeen"`
}

type Channel struct {
	ID       string   `json:"id"`
	Number   int      `json:"number"`
	Minor    int      `json:"minor,omitempty"` // ATSC sub-channel: Number.Minor
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	Services []string `json:"services"` // in failover order
	Icon     string   `json:"icon,omitempty"`
	EPGID    string   `json:"epgId,omitempty"` // XMLTV channel id; empty = use over-the-air EIT
	Radio    bool     `json:"radio"`
	Profile  string   `json:"profile,omitempty"` // transcode profile override for this channel
}

// SatInput configures how an adapter reaches a satellite network.
type SatInput struct {
	LNB        string `json:"lnb"`        // universal, single, circular, none
	LOFLow     uint32 `json:"lofLow"`     // kHz
	LOFHigh    uint32 `json:"lofHigh"`    // kHz
	Switch     uint32 `json:"switch"`     // kHz
	DiseqcPort int    `json:"diseqcPort"` // 0 = none, 1..4 committed port
}

type TunerConfig struct {
	Key         string              `json:"key"` // adapter0/frontend0 or virtual0
	Name        string              `json:"name"`
	Enabled     bool                `json:"enabled"`
	Networks    []string            `json:"networks"`
	Priority    int                 `json:"priority"`      // higher is used first
	TuneTimeout int                 `json:"tuneTimeout"`   // seconds
	Sat         map[string]SatInput `json:"sat,omitempty"` // by network id
}

// Profile is a stream output profile. Codec "copy" passes the service
// through untouched; anything else runs ffmpeg.
type Profile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	VideoCodec  string `json:"videoCodec"`  // copy, h264_nvenc, hevc_nvenc, libx264, libx265
	HWDecode    bool   `json:"hwDecode"`    // NVDEC decode (cuda) when using nvenc
	Preset      string `json:"preset"`      // nvenc: p1..p7, x264: ultrafast..veryslow
	RateControl string `json:"rateControl"` // cbr, vbr, cq
	BitrateK    int    `json:"bitrate"`     // target video kbit/s
	MaxrateK    int    `json:"maxrate"`     // peak kbit/s (vbr)
	BufsizeK    int    `json:"bufsize"`     // VBV buffer kbit
	CQ          int    `json:"cq"`          // quality for cq mode
	Height      int    `json:"height"`      // 0 = keep
	FPS         int    `json:"fps"`         // 0 = keep
	Deinterlace bool   `json:"deinterlace"`
	AudioCodec  string `json:"audioCodec"` // copy, aac, ac3
	AudioK      int    `json:"audioBitrate"`
	AudioCh     int    `json:"audioChannels"`   // 0 = keep
	GPU         int    `json:"gpu"`             // cuda device index
	Extra       string `json:"extra,omitempty"` // extra ffmpeg output args
}

type XMLTVSource struct {
	URL     string `json:"url"` // http(s) URL or local path
	Enabled bool   `json:"enabled"`
}

type Settings struct {
	ServerName    string        `json:"serverName"`
	BaseURL       string        `json:"baseUrl"` // advertised URL; empty = derive from request
	DeviceID      string        `json:"deviceId"`
	SSDP          bool          `json:"ssdp"`
	HDHRProfile   string        `json:"hdhrProfile"` // profile used for the default HDHomeRun/M3U lineup
	XMLTV         []XMLTVSource `json:"xmltv"`
	XMLTVHours    int           `json:"xmltvHours"` // refresh interval
	EIT           bool          `json:"eit"`
	FFmpeg        string        `json:"ffmpeg"`
	VirtualTuners int           `json:"virtualTuners"`
	EPGDays       int           `json:"epgDays"`
}

type State struct {
	Networks map[string]*Network     `json:"networks"`
	Muxes    map[string]*Mux         `json:"muxes"`
	Services map[string]*Service     `json:"services"`
	Channels map[string]*Channel     `json:"channels"`
	Tuners   map[string]*TunerConfig `json:"tuners"`
	Profiles map[string]*Profile     `json:"profiles"`
	Settings Settings                `json:"settings"`
}

func DefaultProfiles() map[string]*Profile {
	return map[string]*Profile{
		"pass": {ID: "pass", Name: "Passthrough", VideoCodec: "copy", AudioCodec: "copy"},
		"nvenc-h264-8m": {ID: "nvenc-h264-8m", Name: "NVENC H.264 1080p 8 Mbit", VideoCodec: "h264_nvenc", HWDecode: true,
			Preset: "p4", RateControl: "vbr", BitrateK: 8000, MaxrateK: 12000, BufsizeK: 16000, Height: 1080,
			Deinterlace: true, AudioCodec: "aac", AudioK: 192, AudioCh: 2},
		"nvenc-h264-3m": {ID: "nvenc-h264-3m", Name: "NVENC H.264 720p 3 Mbit", VideoCodec: "h264_nvenc", HWDecode: true,
			Preset: "p4", RateControl: "cbr", BitrateK: 3000, MaxrateK: 3000, BufsizeK: 6000, Height: 720,
			Deinterlace: true, AudioCodec: "aac", AudioK: 128, AudioCh: 2},
		"nvenc-hevc-4m": {ID: "nvenc-hevc-4m", Name: "NVENC HEVC 1080p 4 Mbit", VideoCodec: "hevc_nvenc", HWDecode: true,
			Preset: "p5", RateControl: "vbr", BitrateK: 4000, MaxrateK: 6000, BufsizeK: 8000, Height: 1080,
			Deinterlace: true, AudioCodec: "aac", AudioK: 160, AudioCh: 2},
		"cpu-h264-2m": {ID: "cpu-h264-2m", Name: "CPU H.264 576p 2 Mbit", VideoCodec: "libx264",
			Preset: "veryfast", RateControl: "vbr", BitrateK: 2000, MaxrateK: 2500, BufsizeK: 4000, Height: 576,
			Deinterlace: true, AudioCodec: "aac", AudioK: 128, AudioCh: 2},
	}
}
