// Package epg stores programme guide data from XMLTV and over-the-air EIT.
package epg

import (
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dvbhub/internal/store"
)

type Event struct {
	ID          string    `json:"id"`
	ChannelID   string    `json:"channelId"`
	Start       time.Time `json:"start"`
	Stop        time.Time `json:"stop"`
	Title       string    `json:"title"`
	Subtitle    string    `json:"subtitle,omitempty"`
	Description string    `json:"description,omitempty"`
	Categories  []string  `json:"categories,omitempty"`
	Season      int       `json:"season,omitempty"`  // 1-based, 0 unknown
	Episode     int       `json:"episode,omitempty"` // 1-based, 0 unknown
	EpisodeText string    `json:"episodeText,omitempty"`
	Rating      string    `json:"rating,omitempty"`
	Icon        string    `json:"icon,omitempty"`
	Source      string    `json:"source"` // eit, xmltv
}

func eventID(ch string, start time.Time) string {
	return ch + "@" + start.UTC().Format("20060102150405")
}

type Guide struct {
	path string

	mu     sync.RWMutex
	byChan map[string][]*Event

	dirty     chan struct{}
	lmu       sync.Mutex
	listeners []func()
	notify    *time.Timer
}

func Open(dir string) *Guide {
	g := &Guide{path: filepath.Join(dir, "epg.json"), byChan: map[string][]*Event{}, dirty: make(chan struct{}, 1)}
	var evs []*Event
	if err := store.ReadJSON(g.path, &evs); err == nil {
		for _, e := range evs {
			g.byChan[e.ChannelID] = append(g.byChan[e.ChannelID], e)
		}
		for ch := range g.byChan {
			g.sortChan(ch)
		}
		log.Printf("epg: loaded %d events", len(evs))
	}
	go g.saver()
	go g.pruner()
	return g
}

// OnUpdate registers fn, called (debounced) after guide changes.
func (g *Guide) OnUpdate(fn func()) {
	g.lmu.Lock()
	g.listeners = append(g.listeners, fn)
	g.lmu.Unlock()
}

func (g *Guide) changed() {
	select {
	case g.dirty <- struct{}{}:
	default:
	}
	g.lmu.Lock()
	defer g.lmu.Unlock()
	if g.notify != nil {
		g.notify.Stop()
	}
	fns := append([]func(){}, g.listeners...)
	g.notify = time.AfterFunc(3*time.Second, func() {
		for _, fn := range fns {
			fn()
		}
	})
}

func (g *Guide) sortChan(ch string) {
	evs := g.byChan[ch]
	sort.Slice(evs, func(i, j int) bool { return evs[i].Start.Before(evs[j].Start) })
}

// Put inserts events for a channel from one source, replacing anything they overlap.
func (g *Guide) Put(ch string, evs []*Event) {
	if len(evs) == 0 {
		return
	}
	g.mu.Lock()
	cur := g.byChan[ch]
	keep := cur[:0:0]
	for _, old := range cur {
		overl := false
		for _, e := range evs {
			if old.Start.Before(e.Stop) && e.Start.Before(old.Stop) {
				overl = true
				break
			}
		}
		if !overl {
			keep = append(keep, old)
		}
	}
	for _, e := range evs {
		e.ChannelID = ch
		e.ID = eventID(ch, e.Start)
		keep = append(keep, e)
	}
	g.byChan[ch] = keep
	g.sortChan(ch)
	g.mu.Unlock()
	g.changed()
}

// ReplaceSource replaces every event of a source on a channel.
func (g *Guide) ReplaceSource(ch, source string, evs []*Event) {
	g.mu.Lock()
	var keep []*Event
	for _, old := range g.byChan[ch] {
		if old.Source != source {
			keep = append(keep, old)
		}
	}
	g.byChan[ch] = keep
	g.mu.Unlock()
	if len(evs) > 0 {
		g.Put(ch, evs)
	} else {
		g.changed()
	}
}

// ClearChannel removes all events for channels no longer present.
func (g *Guide) Retain(valid map[string]bool) {
	g.mu.Lock()
	for ch := range g.byChan {
		if !valid[ch] {
			delete(g.byChan, ch)
		}
	}
	g.mu.Unlock()
}

// Range returns events of a channel overlapping [from, to).
func (g *Guide) Range(ch string, from, to time.Time) []Event {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := []Event{}
	for _, e := range g.byChan[ch] {
		if e.Stop.After(from) && e.Start.Before(to) {
			out = append(out, *e)
		}
	}
	return out
}

// Get finds an event by id.
func (g *Guide) Get(id string) (Event, bool) {
	ch, _, _ := strings.Cut(id, "@")
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, e := range g.byChan[ch] {
		if e.ID == id {
			return *e, true
		}
	}
	return Event{}, false
}

// NowNext returns the current and next event for a channel.
func (g *Guide) NowNext(ch string, at time.Time) (now, next *Event) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, e := range g.byChan[ch] {
		if !e.Stop.After(at) {
			continue
		}
		c := *e
		if now == nil && !e.Start.After(at) {
			now = &c
			continue
		}
		next = &c
		break
	}
	return
}

// Search finds future events whose title contains q (case-insensitive).
func (g *Guide) Search(q string, from time.Time, limit int) []Event {
	q = strings.ToLower(q)
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []Event
	for _, evs := range g.byChan {
		for _, e := range evs {
			if e.Stop.After(from) && strings.Contains(strings.ToLower(e.Title), q) {
				out = append(out, *e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// All returns every future event (used by DVR rule matching and export).
func (g *Guide) All(from time.Time) []Event {
	g.mu.RLock()
	defer g.mu.RUnlock()
	var out []Event
	for _, evs := range g.byChan {
		for _, e := range evs {
			if e.Stop.After(from) {
				out = append(out, *e)
			}
		}
	}
	return out
}

func (g *Guide) Count() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := 0
	for _, evs := range g.byChan {
		n += len(evs)
	}
	return n
}

func (g *Guide) saver() {
	for range g.dirty {
		time.Sleep(10 * time.Second)
		g.mu.RLock()
		var all []*Event
		for _, evs := range g.byChan {
			all = append(all, evs...)
		}
		err := store.WriteJSON(g.path, all)
		g.mu.RUnlock()
		if err != nil {
			log.Printf("epg: save: %v", err)
		}
	}
}

func (g *Guide) pruner() {
	for range time.Tick(time.Hour) {
		cut := time.Now().Add(-12 * time.Hour)
		g.mu.Lock()
		for ch, evs := range g.byChan {
			keep := evs[:0]
			for _, e := range evs {
				if e.Stop.After(cut) {
					keep = append(keep, e)
				}
			}
			g.byChan[ch] = keep
		}
		g.mu.Unlock()
		g.changed()
	}
}
