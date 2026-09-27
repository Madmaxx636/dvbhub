package epg

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dvbhub/internal/store"
	"dvbhub/internal/ts"
	"dvbhub/internal/tuner"
)

type eitBatch struct {
	mux  string
	pkts []byte
}

// EITGrabber collects over-the-air EPG from every tuned mux and
// periodically tunes idle muxes to refresh schedules.
type EITGrabber struct {
	st    *store.Store
	tm    *tuner.Manager
	guide *Guide

	enabled atomic.Bool
	in      chan eitBatch

	mu      sync.Mutex
	idx     map[string][]string // "onid:tsid:sid" -> channel ids
	idxSig  string
	grabbed map[string]time.Time
	gen     atomic.Uint64 // bumped when the service->channel mapping changes
	kick    chan struct{}
	Events  atomic.Uint64
}

func NewEITGrabber(st *store.Store, tm *tuner.Manager, g *Guide) *EITGrabber {
	e := &EITGrabber{st: st, tm: tm, guide: g, in: make(chan eitBatch, 1024), grabbed: map[string]time.Time{},
		kick: make(chan struct{}, 1)}
	e.refresh()
	st.OnChange(e.refresh)
	tm.AddTap(e.tap)
	go e.worker()
	go e.idleLoop()
	return e
}

// refresh rebuilds the service->channel index. When the mapping changes,
// already-seen EIT sections are re-processed and idle muxes are re-grabbed,
// so newly mapped channels get guide data promptly.
func (e *EITGrabber) refresh() {
	var on bool
	idx := map[string][]string{}
	e.st.View(func(s *store.State) {
		on = s.Settings.EIT
		for _, c := range s.Channels {
			if c.EPGID != "" || !c.Enabled {
				continue // channel uses XMLTV
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
				k := svcKey(mux.ONID, mux.TSID, svc.SID)
				idx[k] = append(idx[k], c.ID)
			}
		}
	})
	keys := make([]string, 0, len(idx))
	for k, v := range idx {
		keys = append(keys, k+"="+strings.Join(v, ","))
	}
	sort.Strings(keys)
	sig := strings.Join(keys, ";")
	e.enabled.Store(on)
	e.mu.Lock()
	changed := sig != e.idxSig
	e.idx, e.idxSig = idx, sig
	if changed {
		e.grabbed = map[string]time.Time{}
	}
	e.mu.Unlock()
	if changed && len(idx) > 0 {
		e.gen.Add(1)
		select {
		case e.kick <- struct{}{}:
		default:
		}
	}
}

func (e *EITGrabber) tap(muxID string, pkts []byte) {
	if !e.enabled.Load() {
		return
	}
	var out []byte
	ts.ForEach(pkts, func(p []byte) {
		if ts.PID(p) == ts.PIDEIT {
			out = append(out, p...)
		}
	})
	if len(out) == 0 {
		return
	}
	select {
	case e.in <- eitBatch{muxID, out}:
	default:
	}
}

func svcKey(onid, tsid, sid uint16) string { return fmt.Sprintf("%d:%d:%d", onid, tsid, sid) }

func (e *EITGrabber) channelsFor(onid, tsid, sid uint16) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.idx[svcKey(onid, tsid, sid)]
}

func (e *EITGrabber) worker() {
	asm := map[string]*ts.SectionAssembler{}
	seen := map[string]bool{}
	gen := e.gen.Load()
	for b := range e.in {
		if g := e.gen.Load(); g != gen {
			gen, seen = g, map[string]bool{}
		}
		a := asm[b.mux]
		if a == nil {
			a = ts.NewSectionAssembler()
			asm[b.mux] = a
		}
		ts.ForEach(b.pkts, func(p []byte) {
			a.Push(p, func(sec []byte) {
				if len(sec) < 14 || !ts.IsEIT(sec[0]) {
					return
				}
				// Skip sections we already processed (same version) cheaply before CRC/parse.
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
				e.apply(eit)
			})
		})
	}
}

func (e *EITGrabber) apply(eit *ts.EIT) {
	chans := e.channelsFor(eit.ONID, eit.TSID, eit.SID)
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
			out.Description = ev.Subtitle
		}
		for _, g := range ev.Genres {
			if n := GenreName(g); n != "" {
				out.Categories = append(out.Categories, n)
			}
		}
		if ev.Rating > 0 {
			out.Rating = fmt.Sprintf("%d+", ev.Rating)
		}
		evs = append(evs, out)
	}
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

// GenreName is re-exported for convenience.
func GenreName(g byte) string { return ts.GenreName(g) }

// idleLoop tunes muxes nobody is watching to refresh EIT schedules.
func (e *EITGrabber) idleLoop() {
	time.Sleep(10 * time.Second)
	for {
		if e.enabled.Load() {
			e.grabIdle()
		}
		select {
		case <-e.kick:
			time.Sleep(5 * time.Second) // let a burst of config changes settle
		case <-time.After(10 * time.Minute):
		}
	}
}

func (e *EITGrabber) grabIdle() {
	var muxes []string
	e.st.View(func(s *store.State) {
		for id, m := range s.Muxes {
			if m.Enabled && m.ScanStatus == "ok" {
				muxes = append(muxes, id)
			}
		}
	})
	busy := e.tm.BusyMuxes()
	for _, id := range muxes {
		e.mu.Lock()
		last := e.grabbed[id]
		e.mu.Unlock()
		if time.Since(last) < 6*time.Hour {
			continue
		}
		if busy[id] { // already being received, the tap collects it
			e.mu.Lock()
			e.grabbed[id] = time.Now()
			e.mu.Unlock()
			continue
		}
		sub, err := e.tm.Subscribe(tuner.Request{MuxID: id, Weight: tuner.WeightEPG, Name: "epg grab"})
		if err != nil {
			continue
		}
		before := e.Events.Load()
		timer := time.NewTimer(75 * time.Second)
	drain:
		for {
			select {
			case <-sub.C:
			case <-sub.Done:
				break drain
			case <-timer.C:
				break drain
			}
		}
		timer.Stop()
		sub.Close()
		e.mu.Lock()
		e.grabbed[id] = time.Now()
		e.mu.Unlock()
		log.Printf("epg: idle grab on mux %s collected %d events", id, e.Events.Load()-before)
	}
}
