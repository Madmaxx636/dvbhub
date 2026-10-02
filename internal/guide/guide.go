// Package guide stores the programme guide, collects it from the broadcast
// (DVB EIT and ATSC PSIP) and XMLTV files, and exports it as XMLTV.
package guide

import (
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"dvbhub/internal/config"
)

// Event is one programme on one channel.
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

// Guide holds events per channel and saves them (debounced) to epg.json.
type Guide struct {
	path string

	mu     sync.RWMutex
	byChan map[string][]*Event

	dirty chan struct{}
}

// Open loads <dir>/epg.json.
func Open(dir string) *Guide {
	g := &Guide{path: filepath.Join(dir, "epg.json"), byChan: map[string][]*Event{}, dirty: make(chan struct{}, 1)}
	var evs []*Event
	if err := config.ReadJSON(g.path, &evs); err == nil {
		cut := time.Now().Add(-12 * time.Hour)
		for _, e := range evs {
			if e.Stop.After(cut) {
				g.byChan[e.ChannelID] = append(g.byChan[e.ChannelID], e)
			}
		}
		for ch := range g.byChan {
			g.sortChan(ch)
		}
		log.Printf("guide: loaded %d programmes", g.Count())
	}
	go g.saver()
	go g.pruner()
	return g
}

func (g *Guide) changed() {
	select {
	case g.dirty <- struct{}{}:
	default:
	}
}

func (g *Guide) sortChan(ch string) {
	evs := g.byChan[ch]
	sort.Slice(evs, func(i, j int) bool { return evs[i].Start.Before(evs[j].Start) })
}

// Put inserts events for a channel, replacing whatever they overlap.
func (g *Guide) Put(ch string, evs []*Event) {
	if len(evs) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.putLocked(ch, evs)
	g.changed()
}

func (g *Guide) putLocked(ch string, evs []*Event) {
	cur := g.byChan[ch]
	keep := cur[:0:0]
	for _, old := range cur {
		overlap := false
		for _, e := range evs {
			if old.Start.Before(e.Stop) && e.Start.Before(old.Stop) {
				overlap = true
				break
			}
		}
		if !overlap {
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
}

// ReplaceSource replaces every event of one source on a channel.
func (g *Guide) ReplaceSource(ch, source string, evs []*Event) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var keep []*Event
	for _, old := range g.byChan[ch] {
		if old.Source != source {
			keep = append(keep, old)
		}
	}
	g.byChan[ch] = keep
	if len(evs) > 0 {
		g.putLocked(ch, evs)
	}
	g.changed()
}

// Retain drops the events of channels that no longer exist.
func (g *Guide) Retain(valid map[string]bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := len(g.byChan)
	for ch := range g.byChan {
		if !valid[ch] {
			delete(g.byChan, ch)
		}
	}
	if len(g.byChan) != n {
		g.changed()
	}
}

// Clear removes every event of a channel (or of all channels when ch is "").
func (g *Guide) Clear(ch string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ch == "" {
		g.byChan = map[string][]*Event{}
	} else {
		delete(g.byChan, ch)
	}
	g.changed()
}

// Range returns a channel's events overlapping [from, to).
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

// NowNext returns a channel's current and next event.
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

// Search finds events that haven't ended whose title contains q.
func (g *Guide) Search(q string, from time.Time, limit int) []Event {
	q = strings.ToLower(strings.TrimSpace(q))
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := []Event{}
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

// Count returns the number of stored events.
func (g *Guide) Count() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := 0
	for _, evs := range g.byChan {
		n += len(evs)
	}
	return n
}

// Coverage returns, per channel, how far ahead the guide reaches.
func (g *Guide) Coverage() map[string]time.Time {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := map[string]time.Time{}
	for ch, evs := range g.byChan {
		if len(evs) > 0 {
			out[ch] = evs[len(evs)-1].Stop
		}
	}
	return out
}

func (g *Guide) saver() {
	for range g.dirty {
		time.Sleep(10 * time.Second)
		g.mu.RLock()
		all := []*Event{}
		for _, evs := range g.byChan {
			all = append(all, evs...)
		}
		err := config.WriteJSON(g.path, all)
		g.mu.RUnlock()
		if err != nil {
			log.Printf("guide: save failed: %v", err)
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
