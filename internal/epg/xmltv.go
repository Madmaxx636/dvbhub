package epg

import (
	"bufio"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dvbhub/internal/store"
)

// XMLTVChannel is a channel listed in an imported XMLTV file.
type XMLTVChannel struct {
	ID     string   `json:"id"`
	Names  []string `json:"names"`
	Icon   string   `json:"icon,omitempty"`
	Source string   `json:"source"`
}

type xmlChannel struct {
	ID    string   `xml:"id,attr"`
	Names []string `xml:"display-name"`
	Icon  struct {
		Src string `xml:"src,attr"`
	} `xml:"icon"`
}

type xmlProgramme struct {
	Start      string   `xml:"start,attr"`
	Stop       string   `xml:"stop,attr"`
	Channel    string   `xml:"channel,attr"`
	Title      []string `xml:"title"`
	SubTitle   []string `xml:"sub-title"`
	Desc       []string `xml:"desc"`
	Categories []string `xml:"category"`
	Episodes   []struct {
		System string `xml:"system,attr"`
		Value  string `xml:",chardata"`
	} `xml:"episode-num"`
	Icon struct {
		Src string `xml:"src,attr"`
	} `xml:"icon"`
	Rating []struct {
		Value string `xml:"value"`
	} `xml:"rating"`
}

// Importer periodically loads configured XMLTV sources into the guide.
type Importer struct {
	st    *store.Store
	guide *Guide
	path  string

	mu       sync.Mutex
	channels []XMLTVChannel
	lastRun  time.Time
	lastErr  string
	lastInfo string
	running  bool
	trigger  chan struct{}
}

func NewImporter(st *store.Store, g *Guide) *Importer {
	im := &Importer{st: st, guide: g, path: filepath.Join(st.Dir(), "xmltv-channels.json"), trigger: make(chan struct{}, 1)}
	store.ReadJSON(im.path, &im.channels)
	go im.loop()
	return im
}

func (im *Importer) Trigger() {
	select {
	case im.trigger <- struct{}{}:
	default:
	}
}

type ImportStatus struct {
	LastRun  time.Time `json:"lastRun"`
	LastErr  string    `json:"lastError,omitempty"`
	Info     string    `json:"info"`
	Running  bool      `json:"running"`
	Channels int       `json:"channels"`
}

func (im *Importer) Status() ImportStatus {
	im.mu.Lock()
	defer im.mu.Unlock()
	return ImportStatus{LastRun: im.lastRun, LastErr: im.lastErr, Info: im.lastInfo, Running: im.running, Channels: len(im.channels)}
}

func (im *Importer) Channels() []XMLTVChannel {
	im.mu.Lock()
	defer im.mu.Unlock()
	return append([]XMLTVChannel(nil), im.channels...)
}

func (im *Importer) loop() {
	time.Sleep(5 * time.Second)
	for {
		var hours int
		var srcs []store.XMLTVSource
		im.st.View(func(s *store.State) {
			hours = s.Settings.XMLTVHours
			srcs = append(srcs, s.Settings.XMLTV...)
		})
		if hours <= 0 {
			hours = 12
		}
		im.mu.Lock()
		due := time.Since(im.lastRun) > time.Duration(hours)*time.Hour
		im.mu.Unlock()
		if due && len(srcs) > 0 {
			im.Run()
		}
		select {
		case <-im.trigger:
			im.Run()
		case <-time.After(5 * time.Minute):
		}
	}
}

// Run imports all enabled sources now.
func (im *Importer) Run() {
	im.mu.Lock()
	if im.running {
		im.mu.Unlock()
		return
	}
	im.running = true
	im.mu.Unlock()

	var srcs []store.XMLTVSource
	epgToChans := map[string][]string{}
	im.st.View(func(s *store.State) {
		srcs = append(srcs, s.Settings.XMLTV...)
		for _, c := range s.Channels {
			if c.EPGID != "" {
				epgToChans[c.EPGID] = append(epgToChans[c.EPGID], c.ID)
			}
		}
	})
	var channels []XMLTVChannel
	events := map[string][]*Event{}
	var errs []string
	total := 0
	for _, src := range srcs {
		if !src.Enabled || strings.TrimSpace(src.URL) == "" {
			continue
		}
		chs, n, err := importSource(src.URL, epgToChans, events)
		channels = append(channels, chs...)
		total += n
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", src.URL, err))
		}
	}
	for _, chans := range epgToChans {
		for _, ch := range chans {
			im.guide.ReplaceSource(ch, "xmltv", events[ch])
		}
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].ID < channels[j].ID })
	im.mu.Lock()
	im.running = false
	im.lastRun = time.Now()
	im.lastErr = strings.Join(errs, "; ")
	im.lastInfo = fmt.Sprintf("%d XMLTV channels, %d programmes for mapped channels", len(channels), total)
	if len(channels) > 0 || len(errs) == 0 {
		im.channels = channels
		store.WriteJSON(im.path, channels)
	}
	im.mu.Unlock()
	log.Printf("xmltv: %s %s", im.lastInfo, im.lastErr)
}

func openSource(src string) (io.ReadCloser, error) {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		c := &http.Client{Timeout: 5 * time.Minute}
		resp, err := c.Get(src)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %s", resp.Status)
		}
		return resp.Body, nil
	}
	return os.Open(src)
}

type multiCloser struct {
	io.Reader
	closers []io.Closer
}

func (m multiCloser) Close() error {
	for _, c := range m.closers {
		c.Close()
	}
	return nil
}

func importSource(src string, epgToChans map[string][]string, events map[string][]*Event) ([]XMLTVChannel, int, error) {
	rc, err := openSource(src)
	if err != nil {
		return nil, 0, err
	}
	br := bufio.NewReader(rc)
	var r io.ReadCloser = multiCloser{br, []io.Closer{rc}}
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			rc.Close()
			return nil, 0, err
		}
		r = multiCloser{gz, []io.Closer{gz, rc}}
	}
	defer r.Close()
	return parseXMLTV(r, src, epgToChans, events)
}

func parseXMLTV(r io.Reader, src string, epgToChans map[string][]string, events map[string][]*Event) ([]XMLTVChannel, int, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.CharsetReader = func(label string, in io.Reader) (io.Reader, error) { return in, nil }
	var channels []XMLTVChannel
	n := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return channels, n, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "channel":
			var c xmlChannel
			if err := dec.DecodeElement(&c, &se); err == nil {
				channels = append(channels, XMLTVChannel{ID: c.ID, Names: c.Names, Icon: c.Icon.Src, Source: src})
			}
		case "programme":
			var p xmlProgramme
			if err := dec.DecodeElement(&p, &se); err != nil {
				continue
			}
			chans := epgToChans[p.Channel]
			if len(chans) == 0 {
				continue
			}
			ev := programmeToEvent(&p)
			if ev == nil {
				continue
			}
			for _, ch := range chans {
				c := *ev
				events[ch] = append(events[ch], &c)
			}
			n++
		}
	}
	return channels, n, nil
}

func first(v []string) string {
	if len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

func programmeToEvent(p *xmlProgramme) *Event {
	start, err := ParseXMLTVTime(p.Start)
	if err != nil {
		return nil
	}
	stop, err := ParseXMLTVTime(p.Stop)
	if err != nil || !stop.After(start) {
		stop = start.Add(30 * time.Minute)
	}
	ev := &Event{Start: start, Stop: stop, Title: first(p.Title), Subtitle: first(p.SubTitle), Description: first(p.Desc),
		Categories: p.Categories, Icon: p.Icon.Src, Source: "xmltv"}
	if len(p.Rating) > 0 {
		ev.Rating = p.Rating[0].Value
	}
	for _, e := range p.Episodes {
		switch e.System {
		case "xmltv_ns":
			parts := strings.Split(strings.ReplaceAll(e.Value, " ", ""), ".")
			if len(parts) >= 2 {
				if s, err := strconv.Atoi(strings.Split(parts[0], "/")[0]); err == nil {
					ev.Season = s + 1
				}
				if ep, err := strconv.Atoi(strings.Split(parts[1], "/")[0]); err == nil {
					ev.Episode = ep + 1
				}
			}
		case "onscreen":
			ev.EpisodeText = strings.TrimSpace(e.Value)
		}
	}
	if ev.EpisodeText == "" && ev.Season > 0 && ev.Episode > 0 {
		ev.EpisodeText = fmt.Sprintf("S%02dE%02d", ev.Season, ev.Episode)
	}
	return ev
}

// ParseXMLTVTime parses "20260927183000 +0100" (offset optional).
func ParseXMLTVTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"20060102150405 -0700", "20060102150405 MST", "20060102150405", "200601021504 -0700", "200601021504"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad xmltv time %q", s)
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func normName(s string) string {
	s = nonAlnum.ReplaceAllString(strings.ToLower(s), "")
	return strings.TrimSuffix(s, "hd")
}

// AutoMap assigns XMLTV ids to channels without one by matching names.
func (im *Importer) AutoMap() int {
	idx := map[string]string{}
	for _, c := range im.Channels() {
		for _, n := range append(c.Names, c.ID) {
			if k := normName(n); k != "" {
				if _, dup := idx[k]; !dup {
					idx[k] = c.ID
				}
			}
		}
	}
	n := 0
	im.st.Update(func(s *store.State) error {
		for _, c := range s.Channels {
			if c.EPGID != "" {
				continue
			}
			if id, ok := idx[normName(c.Name)]; ok {
				c.EPGID = id
				n++
			}
		}
		return nil
	})
	if n > 0 {
		im.Trigger()
	}
	return n
}

// ---- export ----

// GuideNumber is the channel id used in exported lineups and XMLTV.
func GuideNumber(c *store.Channel) string { return strconv.Itoa(c.Number) }

func xmlEsc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// WriteXMLTV writes an XMLTV document for all enabled channels.
func (g *Guide) WriteXMLTV(w io.Writer, chans []*store.Channel, iconURL func(*store.Channel) string) error {
	bw := bufio.NewWriter(w)
	fmt.Fprint(bw, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE tv SYSTEM \"xmltv.dtd\">\n<tv generator-info-name=\"dvbhub\">\n")
	for _, c := range chans {
		fmt.Fprintf(bw, "  <channel id=\"%s\">\n    <display-name>%s</display-name>\n    <display-name>%s</display-name>\n",
			xmlEsc(GuideNumber(c)), xmlEsc(c.Name), xmlEsc(GuideNumber(c)))
		if u := iconURL(c); u != "" {
			fmt.Fprintf(bw, "    <icon src=\"%s\"/>\n", xmlEsc(u))
		}
		fmt.Fprintf(bw, "    <lcn>%d</lcn>\n  </channel>\n", c.Number)
	}
	now := time.Now().Add(-6 * time.Hour)
	for _, c := range chans {
		for _, e := range g.Range(c.ID, now, now.Add(30*24*time.Hour)) {
			fmt.Fprintf(bw, "  <programme start=\"%s\" stop=\"%s\" channel=\"%s\">\n", e.Start.Format("20060102150405 -0700"),
				e.Stop.Format("20060102150405 -0700"), xmlEsc(GuideNumber(c)))
			fmt.Fprintf(bw, "    <title>%s</title>\n", xmlEsc(e.Title))
			if e.Subtitle != "" {
				fmt.Fprintf(bw, "    <sub-title>%s</sub-title>\n", xmlEsc(e.Subtitle))
			}
			if e.Description != "" {
				fmt.Fprintf(bw, "    <desc>%s</desc>\n", xmlEsc(e.Description))
			}
			for _, cat := range e.Categories {
				fmt.Fprintf(bw, "    <category>%s</category>\n", xmlEsc(cat))
			}
			if e.Icon != "" {
				fmt.Fprintf(bw, "    <icon src=\"%s\"/>\n", xmlEsc(e.Icon))
			}
			if e.Season > 0 || e.Episode > 0 {
				s, ep := max(e.Season-1, 0), max(e.Episode-1, 0)
				if e.Season > 0 {
					fmt.Fprintf(bw, "    <episode-num system=\"xmltv_ns\">%d.%d.</episode-num>\n", s, ep)
				} else {
					fmt.Fprintf(bw, "    <episode-num system=\"xmltv_ns\">.%d.</episode-num>\n", ep)
				}
			}
			if e.EpisodeText != "" {
				fmt.Fprintf(bw, "    <episode-num system=\"onscreen\">%s</episode-num>\n", xmlEsc(e.EpisodeText))
			}
			if e.Rating != "" {
				fmt.Fprintf(bw, "    <rating><value>%s</value></rating>\n", xmlEsc(e.Rating))
			}
			fmt.Fprint(bw, "  </programme>\n")
		}
	}
	fmt.Fprint(bw, "</tv>\n")
	return bw.Flush()
}
