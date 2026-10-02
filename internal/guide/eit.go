package guide

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dvbhub/internal/config"
	ts "dvbhub/internal/mpegts"
	"dvbhub/internal/tuners"
)

type batch struct {
	mux  string
	pkts []byte
}

// Grabber collects the over-the-air guide from every tuned mux and, every few
// hours, tunes idle muxes for a minute to refresh it.
type Grabber struct {
	st    *config.Store
	tm    *tuners.Manager
	guide *Guide

	enabled atomic.Bool
	in      chan batch

	mu       sync.Mutex
	idx      map[string][]string        // "onid:tsid:sid" -> channel ids (DVB)
	idxProg  map[string][]string        // "muxID:program" -> channel ids (ATSC)
	idxSrc   map[string][]string        // "muxID:sourceID" -> channel ids (ATSC)
	psip     map[string]map[uint16]bool // muxID -> ATSC EIT/ETT PIDs from the MGT
	idxSig   string
	grabbed  map[string]time.Time
	grabbing map[string]bool      // muxes being grabbed now
	lastNew  map[string]time.Time // when a grabbed mux last brought a new section
	observed map[string]bool      // ATSC sections seen at all (worker goroutine only)
	lastRun  time.Time
	gen      atomic.Uint64 // bumped when the service->channel mapping changes
	kick     chan struct{}

	Events atomic.Uint64 // events received since start
}

// GrabberStatus is shown in the UI.
type GrabberStatus struct {
	Enabled  bool      `json:"enabled"`
	Events   uint64    `json:"events"`
	Grabbing string    `json:"grabbing,omitempty"` // mux being grabbed now
	LastRun  time.Time `json:"lastRun"`
	Muxes    int       `json:"muxes"` // muxes grabbed since the mapping last changed
}

func NewGrabber(st *config.Store, tm *tuners.Manager, g *Guide) *Grabber {
	e := &Grabber{st: st, tm: tm, guide: g, in: make(chan batch, 1024), grabbed: map[string]time.Time{},
		grabbing: map[string]bool{}, lastNew: map[string]time.Time{}, observed: map[string]bool{},
		kick: make(chan struct{}, 1), psip: map[string]map[uint16]bool{}}
	e.refresh()
	st.OnChange(e.refresh)
	tm.AddTap(e.tap)
	go e.worker()
	go e.idleLoop()
	return e
}

func (e *Grabber) Status() GrabberStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	var now []string
	for id := range e.grabbing {
		now = append(now, e.muxName(id))
	}
	sort.Strings(now)
	return GrabberStatus{Enabled: e.enabled.Load(), Events: e.Events.Load(), Grabbing: strings.Join(now, ", "), LastRun: e.lastRun, Muxes: len(e.grabbed)}
}

// GrabNow forgets when muxes were last grabbed and starts a round.
func (e *Grabber) GrabNow() {
	e.mu.Lock()
	e.grabbed = map[string]time.Time{}
	e.mu.Unlock()
	e.gen.Add(1)
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// refresh rebuilds the service->channel index. When the mapping changes,
// sections already seen are processed again and idle muxes grabbed again,
// so newly mapped channels get their guide quickly.
func (e *Grabber) refresh() {
	var on bool
	idx := map[string][]string{}
	idxProg := map[string][]string{}
	idxSrc := map[string][]string{}
	e.st.View(func(s *config.State) {
		on = s.Settings.EIT
		for _, c := range s.Channels {
			if c.GuideID != "" || !c.Enabled {
				continue // this channel's guide comes from XMLTV
			}
			for _, sid := range c.Services {
				svc, ok := s.Services[sid]
				if !ok {
					continue
				}
				mux, ok := s.Muxes[svc.MuxID]
				if !ok {
					continue
				}
				k := fmt.Sprintf("%d:%d:%d", mux.ONID, mux.TSID, svc.SID)
				idx[k] = append(idx[k], c.ID)
				pk := fmt.Sprintf("%s:%d", mux.ID, svc.SID)
				idxProg[pk] = append(idxProg[pk], c.ID)
				if svc.SourceID != 0 {
					sk := fmt.Sprintf("%s:%d", mux.ID, svc.SourceID)
					idxSrc[sk] = append(idxSrc[sk], c.ID)
				}
			}
		}
	})
	keys := make([]string, 0, len(idx))
	for k, v := range idxProg {
		keys = append(keys, k+"="+strings.Join(v, ","))
	}
	sort.Strings(keys)
	sig := strings.Join(keys, ";")
	e.enabled.Store(on)
	e.mu.Lock()
	changed := sig != e.idxSig
	e.idx, e.idxProg, e.idxSrc, e.idxSig = idx, idxProg, idxSrc, sig
	if changed {
		e.grabbed = map[string]time.Time{}
	}
	e.mu.Unlock()
	if changed && len(idxProg) > 0 {
		e.gen.Add(1)
		select {
		case e.kick <- struct{}{}:
		default:
		}
	}
}

// tap receives every chunk read from any mux; it keeps only guide packets.
func (e *Grabber) tap(muxID string, pkts []byte) {
	if !e.enabled.Load() {
		return
	}
	e.mu.Lock()
	psip := e.psip[muxID]
	e.mu.Unlock()
	var out []byte
	ts.ForEach(pkts, func(p []byte) {
		if pid := ts.PID(p); pid == ts.PIDEIT || pid == ts.PIDPSIP || psip[pid] {
			out = append(out, p...)
		}
	})
	if len(out) == 0 {
		return
	}
	select {
	case e.in <- batch{muxID, out}:
	default:
	}
}

func (e *Grabber) lookup(m map[string][]string, key string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return m[key]
}

func (e *Grabber) worker() {
	asm := map[string]*ts.SectionAssembler{}
	seen := map[string]bool{}
	gen := e.gen.Load()
	atsc := map[string]*atscMux{}
	for b := range e.in {
		if g := e.gen.Load(); g != gen {
			gen, seen = g, map[string]bool{}
		}
		ts.ForEach(b.pkts, func(p []byte) {
			pid := ts.PID(p)
			ak := fmt.Sprintf("%s:%d", b.mux, pid)
			a := asm[ak]
			if a == nil {
				a = ts.NewSectionAssembler()
				asm[ak] = a
			}
			a.Push(p, func(sec []byte) {
				if pid != ts.PIDEIT {
					am := atsc[b.mux]
					if am == nil {
						am = newATSCMux()
						atsc[b.mux] = am
					}
					e.atscSection(b.mux, am, pid, sec, seen)
					return
				}
				if len(sec) < 14 || !ts.IsEIT(sec[0]) {
					return
				}
				// Skip sections already processed (same table, service, version and number).
				key := string(sec[:14])
				if seen[key] {
					return
				}
				ps, err := ts.ParseSection(sec)
				if err != nil {
					return
				}
				eit, err := ts.ParseEIT(ps)
				if err != nil {
					return
				}
				if len(seen) > 200000 {
					seen = map[string]bool{}
				}
				seen[key] = true
				e.markNew(b.mux)
				e.applyDVB(eit)
			})
		})
	}
}

func (e *Grabber) applyDVB(eit *ts.EIT) {
	chans := e.lookup(e.idx, fmt.Sprintf("%d:%d:%d", eit.ONID, eit.TSID, eit.SID))
	if len(chans) == 0 {
		return
	}
	var evs []*Event
	for _, ev := range eit.Events {
		if ev.Start.IsZero() || ev.Duration <= 0 || ev.Title == "" {
			continue
		}
		out := &Event{Start: ev.Start, Stop: ev.Start.Add(ev.Duration), Title: ev.Title, Source: "eit"}
		if ev.Description != "" {
			out.Subtitle, out.Description = ev.Subtitle, ev.Description
		} else {
			out.Description = ev.Subtitle // the short text is usually a synopsis
		}
		for _, g := range ev.Genres {
			if n := ts.GenreName(g); n != "" && !contains(out.Categories, n) {
				out.Categories = append(out.Categories, n)
			}
		}
		if ev.Rating > 0 {
			out.Rating = fmt.Sprintf("%d+", ev.Rating)
		}
		evs = append(evs, out)
	}
	e.put(chans, evs)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (e *Grabber) put(chans []string, evs []*Event) {
	if len(evs) == 0 {
		return
	}
	e.Events.Add(uint64(len(evs)))
	for _, ch := range chans {
		cp := make([]*Event, len(evs))
		for i, ev := range evs {
			c := *ev
			cp[i] = &c
		}
		e.guide.Put(ch, cp)
	}
}

// idleLoop tunes muxes nobody is watching to refresh the guide.
func (e *Grabber) idleLoop() {
	time.Sleep(10 * time.Second)
	for {
		if e.enabled.Load() {
			e.grabIdle()
		}
		select {
		case <-e.kick:
			time.Sleep(5 * time.Second) // let a burst of changes settle
		case <-time.After(10 * time.Minute):
		}
	}
}

func (e *Grabber) grabIdle() {
	var muxes []string
	every := 6 * time.Hour
	e.st.View(func(s *config.State) {
		if s.Settings.GuideGrabHours > 0 {
			every = time.Duration(s.Settings.GuideGrabHours) * time.Hour
		} else {
			every = 0
		}
		used := map[string]bool{}
		for _, c := range s.Channels {
			if c.Enabled && c.GuideID == "" {
				for _, sid := range c.Services {
					if svc, ok := s.Services[sid]; ok {
						used[svc.MuxID] = true
					}
				}
			}
		}
		for id, m := range s.Muxes {
			if m.Enabled && m.Scan.Status == "ok" && used[id] {
				muxes = append(muxes, id)
			}
		}
	})
	if every == 0 {
		return // idle grabbing is off; the guide still comes from muxes being watched
	}
	sort.Strings(muxes)
	busy := e.tm.BusyMuxes()
	// Grab on as many tuners as are free; viewers can still take them over.
	slots := make(chan struct{}, max(1, e.tm.TunerCount()))
	var wg sync.WaitGroup
	for _, id := range muxes {
		e.mu.Lock()
		last := e.grabbed[id]
		e.mu.Unlock()
		if time.Since(last) < every {
			continue
		}
		if busy[id] { // already being received: the tap collects it
			e.mu.Lock()
			e.grabbed[id] = time.Now()
			e.mu.Unlock()
			continue
		}
		slots <- struct{}{}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-slots }()
			e.grabOne(id)
		}(id)
	}
	wg.Wait()
}

const (
	grabMin  = 30 * time.Second // ATSC repeats its later guide tables about once a minute at most
	grabIdle = 20 * time.Second // stop once nothing new arrived for this long
	grabMax  = 90 * time.Second
)

// grabOne tunes a mux until its guide stops bringing anything new.
func (e *Grabber) grabOne(id string) {
	sub, err := e.tm.Subscribe(tuners.Request{MuxID: id, Weight: tuners.WeightEPG, Name: "guide"})
	if err != nil {
		return // no free tuner: try again next round
	}
	start := time.Now()
	e.mu.Lock()
	e.grabbing[id] = true
	e.lastNew[id] = start
	e.mu.Unlock()
	before := e.Events.Load()
	tick := time.NewTicker(time.Second)
drain:
	for {
		select {
		case <-sub.C:
		case <-sub.Done:
			break drain
		case <-tick.C:
			e.mu.Lock()
			quiet := time.Since(e.lastNew[id])
			e.mu.Unlock()
			if el := time.Since(start); el > grabMax || (el > grabMin && quiet > grabIdle) {
				break drain
			}
		}
	}
	tick.Stop()
	sub.Close()
	e.mu.Lock()
	delete(e.grabbing, id)
	e.grabbed[id] = time.Now()
	e.lastRun = time.Now()
	e.mu.Unlock()
	log.Printf("guide: collected %d programme updates from %s in %s", e.Events.Load()-before, e.muxName(id), time.Since(start).Round(time.Second))
}

func (e *Grabber) muxName(id string) string {
	name := id
	e.st.View(func(s *config.State) {
		if m, ok := s.Muxes[id]; ok {
			name = tuners.MuxLabel(*m)
		}
	})
	return name
}

// markNew notes that a mux delivered a guide section not seen before.
func (e *Grabber) markNew(muxID string) {
	e.mu.Lock()
	if _, ok := e.lastNew[muxID]; ok {
		e.lastNew[muxID] = time.Now()
	}
	e.mu.Unlock()
}
