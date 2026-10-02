package guide

import (
	"fmt"

	ts "dvbhub/internal/mpegts"
)

// ATSC guide: the MGT lists the PIDs of EIT-0..127 (titles, 3 hours each)
// and ETT tables (descriptions). Events are keyed by source_id, which the
// scanner stored on each service from the VCT; a VCT seen in the stream is
// used as a fallback.

type atscKey struct{ source, event uint16 }

type atscEvent struct {
	ev    Event
	chans []string
}

type atscMux struct {
	offset  int
	sources map[uint16]uint16 // source_id -> program number (from the VCT)
	events  map[atscKey]*atscEvent
	ett     map[atscKey]string // descriptions that arrived before their event
}

func newATSCMux() *atscMux {
	return &atscMux{offset: ts.DefaultGPSUTCOffset, sources: map[uint16]uint16{},
		events: map[atscKey]*atscEvent{}, ett: map[atscKey]string{}}
}

func (e *Grabber) atscChannels(muxID string, am *atscMux, source uint16) []string {
	if chans := e.lookup(e.idxSrc, fmt.Sprintf("%s:%d", muxID, source)); len(chans) > 0 {
		return chans
	}
	if prog, ok := am.sources[source]; ok {
		return e.lookup(e.idxProg, fmt.Sprintf("%s:%d", muxID, prog))
	}
	return nil
}

func (e *Grabber) atscSection(muxID string, am *atscMux, pid uint16, sec []byte, seen map[string]bool) {
	if len(sec) < 3 {
		return
	}
	tid := sec[0]
	var key string
	if tid == ts.TableEIT || tid == ts.TableETT {
		key = fmt.Sprintf("%s:%d:%x", muxID, pid, sec[:min(len(sec), 13)])
		if seen[key] {
			return
		}
	}
	ps, err := ts.ParseSection(sec)
	if err != nil || !ps.Current {
		return
	}
	if key != "" && !e.observed[key] {
		if len(e.observed) > 200000 {
			e.observed = map[string]bool{}
		}
		e.observed[key] = true // first time this section version was seen at all
		e.markNew(muxID)
	}
	switch {
	case pid == ts.PIDPSIP && tid == ts.TableMGT:
		tables, err := ts.ParseMGT(ps)
		if err != nil {
			return
		}
		pids := map[uint16]bool{}
		for _, t := range tables {
			if t.IsGuide() {
				pids[t.PID] = true
			}
		}
		e.mu.Lock()
		e.psip[muxID] = pids
		e.mu.Unlock()
	case pid == ts.PIDPSIP && tid == ts.TableSTT:
		if off, err := ts.ParseSTT(ps); err == nil && off > 0 && off < 60 {
			am.offset = off
		}
	case pid == ts.PIDPSIP && ts.IsVCT(tid):
		if v, err := ts.ParseVCT(ps); err == nil {
			for _, c := range v.Channels {
				am.sources[c.SourceID] = c.Program
			}
		}
	case tid == ts.TableEIT:
		eit, err := ts.ParseATSCEIT(ps)
		if err != nil {
			return
		}
		chans := e.atscChannels(muxID, am, eit.SourceID)
		if len(chans) == 0 {
			return // not mapped (or VCT not seen yet): retry on the next repetition
		}
		seen[key] = true
		var evs []*Event
		for _, ae := range eit.Events {
			if ae.Title == "" || ae.Duration <= 0 {
				continue
			}
			start := ts.GPSTime(ae.Start, am.offset)
			k := atscKey{eit.SourceID, ae.EventID}
			ev := Event{Start: start, Stop: start.Add(ae.Duration), Title: ae.Title, Source: "eit"}
			if d, ok := am.ett[k]; ok {
				ev.Description = d
			} else if old := am.events[k]; old != nil {
				ev.Description = old.ev.Description
			}
			am.events[k] = &atscEvent{ev: ev, chans: chans}
			c := ev
			evs = append(evs, &c)
		}
		if len(am.events) > 50000 {
			am.events = map[atscKey]*atscEvent{}
		}
		e.put(chans, evs)
	case tid == ts.TableETT:
		ett, err := ts.ParseETT(ps)
		if err != nil || ett.Text == "" {
			return
		}
		k := atscKey{ett.SourceID, ett.EventID}
		ae := am.events[k]
		if ae == nil {
			if len(am.ett) > 50000 {
				am.ett = map[atscKey]string{}
			}
			am.ett[k] = ett.Text
			return
		}
		seen[key] = true
		if ae.ev.Description == ett.Text {
			return
		}
		ae.ev.Description = ett.Text
		for _, ch := range ae.chans {
			c := ae.ev
			e.guide.Put(ch, []*Event{&c})
		}
	}
}
