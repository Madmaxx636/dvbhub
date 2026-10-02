package server

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/guide"
	"dvbhub/internal/transcode"
)

// ---- profiles ----

func (s *Server) apiProfiles(w http.ResponseWriter, r *http.Request) {
	type profOut struct {
		config.Profile
		Passthrough bool             `json:"passthrough"`
		Plans       []transcode.Plan `json:"plans,omitempty"` // what would run, best first
		Command     string           `json:"command,omitempty"`
		BuiltIn     bool             `json:"builtIn"`
		Default     bool             `json:"default"`
		Channels    int              `json:"channels"` // channels that use it as their own profile
	}
	ff := s.ffmpegPath()
	caps := s.Encoders.Caps(ff)
	builtins := config.DefaultProfiles()
	out := []profOut{}
	s.Store.View(func(st *config.State) {
		used := map[string]int{}
		for _, c := range st.Channels {
			used[c.Profile]++
		}
		for _, p := range st.Profiles {
			po := profOut{Profile: *p, Passthrough: p.Passthrough(), Default: p.ID == st.Settings.DefaultProfile, Channels: used[p.ID]}
			_, po.BuiltIn = builtins[p.ID]
			if !po.Passthrough {
				po.Plans = transcode.Plans(p, st.Settings.HWAccel, &caps)
				po.Command = transcode.CommandLine(ff, p, po.Plans[0], caps.Filters)
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

func oneOf(field, v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("%s must be one of %s", field, strings.Join(allowed, ", "))
}

func validateProfile(p *config.Profile) error {
	for _, chk := range []error{
		oneOf("mode", p.Mode, "passthrough", "transcode", "mpeg2"),
		oneOf("encoder", p.Encoder, "auto", "nvenc", "qsv", "vaapi", "cpu"),
		oneOf("codec", p.Codec, "h264", "hevc", "av1"),
		oneOf("deinterlace", p.Deinterlace, "auto", "on", "off"),
		oneOf("rate control", p.RateControl, "vbr", "cbr", "cq"),
		oneOf("preset", p.Preset, "fast", "balanced", "quality"),
		oneOf("audio codec", p.AudioCodec, "copy", "aac", "ac3", "opus"),
		oneOf("audio tracks", p.AudioTracks, "all", "first"),
		oneOf("subtitles", p.Subtitles, "keep", "drop"),
	} {
		if chk != nil {
			return chk
		}
	}
	switch {
	case p.Height < 0 || p.Height > 4320:
		return errors.New("height must be 0 (keep) to 4320")
	case p.FPS < 0 || p.FPS > 120:
		return errors.New("frame rate must be 0 (keep) to 120")
	case p.BitrateK < 0 || p.BitrateK > 200000 || p.MaxrateK < 0 || p.MaxrateK > 200000 || p.BufsizeK < 0 || p.BufsizeK > 400000:
		return errors.New("bitrates must be 0-200000 kbit/s")
	case !p.Passthrough() && p.RateControl != "cq" && p.BitrateK <= 0:
		return errors.New("a video bitrate is required")
	case p.Quality < 0 || p.Quality > 63:
		return errors.New("quality must be 0-63")
	case p.GOP < 0 || p.GOP > 1000:
		return errors.New("keyframe interval must be 0-1000 frames")
	case p.AudioK < 0 || p.AudioK > 1024 || p.AudioCh < 0 || p.AudioCh > 8:
		return errors.New("audio bitrate must be 0-1024 kbit/s and channels 0-8")
	case p.GPU < 0 || p.GPU > 16:
		return errors.New("GPU number must be 0-16")
	case strings.ContainsAny(p.Extra, ";&|`$<>\\\"'"):
		return errors.New("extra ffmpeg options may not contain shell or quote characters")
	}
	return nil
}

func (s *Server) apiPutProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !profileID.MatchString(id) {
		httpError(w, 400, errors.New("profile id must be lowercase letters, digits, . _ -"))
		return
	}
	body, err := readBody(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var out config.Profile
	err = s.Store.Update(func(st *config.State) error {
		old, ok := st.Profiles[id]
		if !ok {
			// New profiles start from the HD preset so every field has a value.
			old = config.DefaultProfiles()["hd"]
			old.Name = id
		}
		p, err := cloneMerge(old, body)
		if err != nil {
			return err
		}
		p.ID = id
		if strings.TrimSpace(p.Name) == "" {
			p.Name = id
		}
		if err := validateProfile(p); err != nil {
			return err
		}
		st.Profiles[id] = p
		out = *p
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) apiDeleteProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.Store.Update(func(st *config.State) error {
		if st.Settings.DefaultProfile == id {
			return errors.New("this is the default profile; choose another default first")
		}
		if len(st.Profiles) <= 1 {
			return errors.New("at least one profile must remain")
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
	ok(w)
}

// ---- settings ----

func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	var set config.Settings
	s.Store.View(func(st *config.State) { set = st.Settings })
	if set.XMLTV == nil {
		set.XMLTV = []config.XMLTVSource{}
	}
	writeJSON(w, set)
}

func (s *Server) apiPutSettings(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		httpError(w, 400, err)
		return
	}
	var out config.Settings
	var xmltvChanged, ffmpegChanged bool
	err = s.Store.Update(func(st *config.State) error {
		set, err := cloneMerge(&st.Settings, body)
		if err != nil {
			return err
		}
		if _, ok := st.Profiles[set.DefaultProfile]; !ok {
			return errors.New("unknown default profile")
		}
		if set.DeviceID == "" {
			set.DeviceID = st.Settings.DeviceID
		}
		if v, err := strconv.ParseUint(set.DeviceID, 16, 32); err != nil || len(set.DeviceID) != 8 {
			return errors.New("device id must be 8 hex digits")
		} else {
			set.DeviceID = config.HDHRDeviceID(uint32(v))
		}
		if strings.TrimSpace(set.FFmpeg) == "" {
			set.FFmpeg = "ffmpeg"
		}
		if strings.TrimSpace(set.ServerName) == "" {
			set.ServerName = "dvbhub"
		}
		if set.BaseURL != "" && !strings.HasPrefix(set.BaseURL, "http://") && !strings.HasPrefix(set.BaseURL, "https://") {
			return errors.New("the server address must start with http:// or https://")
		}
		for _, chk := range []error{
			oneOf("hardware acceleration", set.HWAccel, "auto", "nvenc", "qsv", "vaapi", "cpu"),
			oneOf("UI level", set.UILevel, "simple", "advanced", "pro"),
		} {
			if chk != nil {
				return chk
			}
		}
		switch {
		case set.VirtualTuners < 0 || set.VirtualTuners > 8:
			return errors.New("test tuners must be 0-8")
		case set.FailoverSecs < 1 || set.FailoverSecs > 300:
			return errors.New("failover delay must be 1-300 seconds")
		case set.MaxOutageSecs < 0 || set.MaxOutageSecs > 3600:
			return errors.New("give-up time must be 0-3600 seconds")
		case set.LingerSecs < 0 || set.LingerSecs > 300:
			return errors.New("keep-tuned time must be 0-300 seconds")
		case set.HotplugSecs < 1 || set.HotplugSecs > 3600:
			return errors.New("tuner check interval must be 1-3600 seconds")
		case set.GuideDays < 1 || set.GuideDays > 31:
			return errors.New("guide days must be 1-31")
		case set.GuideGrabHours < 0 || set.GuideGrabHours > 168:
			return errors.New("guide refresh must be 0-168 hours")
		case set.XMLTVHours < 1 || set.XMLTVHours > 168:
			return errors.New("XMLTV refresh must be 1-168 hours")
		}
		var srcs []config.XMLTVSource
		for _, x := range set.XMLTV {
			if x.URL = strings.TrimSpace(x.URL); x.URL != "" {
				srcs = append(srcs, x)
			}
		}
		set.XMLTV = srcs
		xmltvChanged = fmt.Sprint(set.XMLTV) != fmt.Sprint(st.Settings.XMLTV)
		ffmpegChanged = set.FFmpeg != st.Settings.FFmpeg
		st.Settings = *set
		out = *set
		return nil
	})
	if err != nil {
		httpError(w, 400, err)
		return
	}
	if xmltvChanged {
		s.XMLTV.Trigger()
	}
	if ffmpegChanged {
		go s.Encoders.Probe(out.FFmpeg)
	}
	writeJSON(w, out)
}

// ---- guide ----

func (s *Server) apiGuide(w http.ResponseWriter, r *http.Request) {
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
		ID          string        `json:"id"`
		GuideNumber string        `json:"guideNumber"`
		Name        string        `json:"name"`
		Icon        string        `json:"icon,omitempty"`
		Source      string        `json:"source"`
		Events      []guide.Event `json:"events"`
	}
	out := []row{}
	for _, c := range s.channelsSorted(false) {
		src := "broadcast"
		if c.GuideID != "" {
			src = "xmltv:" + c.GuideID
		}
		out = append(out, row{ID: c.ID, GuideNumber: c.GuideNumber(), Name: c.Name, Icon: c.Icon, Source: src, Events: s.Guide.Range(c.ID, from, to)})
	}
	writeJSON(w, map[string]any{"from": from, "to": to, "channels": out})
}

func (s *Server) apiGuideSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeJSON(w, []guide.Event{})
		return
	}
	writeJSON(w, s.Guide.Search(q, time.Now(), 100))
}

func (s *Server) apiGuideStatus(w http.ResponseWriter, r *http.Request) {
	cover := s.Guide.Coverage()
	type chCover struct {
		ID          string    `json:"id"`
		GuideNumber string    `json:"guideNumber"`
		Name        string    `json:"name"`
		Source      string    `json:"source"`
		Until       time.Time `json:"until"`
	}
	list := []chCover{}
	for _, c := range s.channelsSorted(false) {
		src := "broadcast"
		if c.GuideID != "" {
			src = "xmltv"
		}
		list = append(list, chCover{c.ID, c.GuideNumber(), c.Name, src, cover[c.ID]})
	}
	writeJSON(w, map[string]any{"events": s.Guide.Count(), "ota": s.Grabber.Status(), "xmltv": s.XMLTV.Status(), "channels": list})
}

func (s *Server) apiGuideGrab(w http.ResponseWriter, r *http.Request) {
	s.Grabber.GrabNow()
	s.XMLTV.Trigger()
	ok(w)
}

func (s *Server) apiGuideClear(w http.ResponseWriter, r *http.Request) {
	s.Guide.Clear(r.URL.Query().Get("channel"))
	s.Grabber.GrabNow()
	ok(w)
}

func (s *Server) apiXMLTVChannels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"status": s.XMLTV.Status(), "channels": s.XMLTV.Channels()})
}

func (s *Server) apiXMLTVRefresh(w http.ResponseWriter, r *http.Request) {
	go s.XMLTV.Run()
	ok(w)
}

func (s *Server) apiXMLTVAutoMap(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]int{"mapped": s.XMLTV.AutoMap()})
}
