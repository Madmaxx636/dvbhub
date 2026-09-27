// Package scan tunes muxes and discovers their services from PAT, PMT, SDT
// and NIT tables.
package scan

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"dvbhub/internal/store"
	"dvbhub/internal/ts"
	"dvbhub/internal/tuner"
)

type Scanner struct {
	st *store.Store
	tm *tuner.Manager

	mu     sync.Mutex
	queue  []string
	active map[string]bool
	wake   chan struct{}
}

func New(st *store.Store, tm *tuner.Manager) *Scanner {
	s := &Scanner{st: st, tm: tm, active: map[string]bool{}, wake: make(chan struct{}, 1)}
	// Muxes left pending by a previous run are re-queued.
	var pending []string
	st.View(func(state *store.State) {
		for id, m := range state.Muxes {
			if m.ScanStatus == "pending" || m.ScanStatus == "scanning" {
				pending = append(pending, id)
			}
		}
	})
	s.Enqueue(pending...)
	go s.loop()
	return s
}

// Enqueue schedules muxes for scanning.
func (s *Scanner) Enqueue(ids ...string) {
	if len(ids) == 0 {
		return
	}
	s.st.Update(func(state *store.State) error {
		for _, id := range ids {
			if m, ok := state.Muxes[id]; ok {
				m.ScanStatus = "pending"
				m.ScanError = ""
			}
		}
		return nil
	})
	s.mu.Lock()
	for _, id := range ids {
		if !slices.Contains(s.queue, id) && !s.active[id] {
			s.queue = append(s.queue, id)
		}
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Status returns queued and active mux ids.
func (s *Scanner) Status() (queued, active []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.active {
		active = append(active, id)
	}
	sort.Strings(active)
	return append([]string(nil), s.queue...), active
}

func (s *Scanner) loop() {
	for {
		s.mu.Lock()
		progressed := false
		var remaining []string
		for _, id := range s.queue {
			sub, err := s.tm.Subscribe(tuner.Request{MuxID: id, Weight: tuner.WeightScan, Name: "scan"})
			if err != nil {
				if errors.Is(err, tuner.ErrNoTuner) {
					remaining = append(remaining, id)
					continue
				}
				s.finish(id, nil, err)
				continue
			}
			progressed = true
			s.active[id] = true
			go func(id string, sub *tuner.Subscription) {
				res, err := s.scanMux(id, sub)
				sub.Close()
				s.finish(id, res, err)
				s.mu.Lock()
				delete(s.active, id)
				s.mu.Unlock()
				select {
				case s.wake <- struct{}{}:
				default:
				}
			}(id, sub)
		}
		s.queue = remaining
		s.mu.Unlock()
		_ = progressed
		select {
		case <-s.wake:
		case <-time.After(2 * time.Second):
		}
	}
}

type result struct {
	pat  *ts.PAT
	pmts map[uint16]*ts.PMT
	sdt  *ts.SDT
	nit  *ts.NIT
}

func (s *Scanner) scanMux(muxID string, sub *tuner.Subscription) (*result, error) {
	s.st.Update(func(state *store.State) error {
		if m, ok := state.Muxes[muxID]; ok {
			m.ScanStatus = "scanning"
		}
		return nil
	})
	var timeout time.Duration = 10 * time.Second
	res := &result{pmts: map[uint16]*ts.PMT{}}
	asm := map[uint16]*ts.SectionAssembler{
		ts.PIDPAT: ts.NewSectionAssembler(), ts.PIDSDT: ts.NewSectionAssembler(), ts.PIDNIT: ts.NewSectionAssembler(),
	}
	pmtPID := map[uint16]bool{}
	sdtParts := map[byte]*ts.SDT{}
	var sdtLast byte
	nitParts := map[byte]*ts.NIT{}
	var nitLast byte
	start := time.Now()
	var firstData time.Time
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	complete := func() bool {
		if res.pat == nil {
			return false
		}
		for prog := range res.pat.Programs {
			if prog != 0 && res.pmts[prog] == nil {
				return false
			}
		}
		if len(sdtParts) <= int(sdtLast) {
			return false
		}
		return len(nitParts) > int(nitLast) || time.Since(firstData) > 12*time.Second
	}

	for {
		select {
		case <-sub.Done:
			return nil, fmt.Errorf("subscription ended: %v", sub.Err())
		case <-deadline.C:
			if firstData.IsZero() {
				return nil, errors.New("no data (no lock?)")
			}
			if res.pat == nil {
				return nil, errors.New("no PAT received")
			}
			return s.merge(res, sdtParts, nitParts), nil
		case pkts := <-sub.C:
			if firstData.IsZero() {
				firstData = time.Now()
				// Now that we are locked, give tables up to 20 s to arrive.
				deadline.Reset(20 * time.Second)
			}
			ts.ForEach(pkts, func(p []byte) {
				pid := ts.PID(p)
				a := asm[pid]
				if a == nil {
					return
				}
				a.Push(p, func(sec []byte) {
					ps, err := ts.ParseSection(sec)
					if err != nil || !ps.Current {
						return
					}
					switch {
					case pid == ts.PIDPAT && ps.TableID == 0x00:
						if pat, err := ts.ParsePAT(ps); err == nil && res.pat == nil {
							res.pat = pat
							for prog, pp := range pat.Programs {
								if prog == 0 {
									if pp != ts.PIDNIT {
										asm[pp] = ts.NewSectionAssembler()
									}
									continue
								}
								pmtPID[pp] = true
								if asm[pp] == nil {
									asm[pp] = ts.NewSectionAssembler()
								}
							}
						}
					case ps.TableID == 0x02 && pmtPID[pid]:
						if pmt, err := ts.ParsePMT(ps); err == nil {
							res.pmts[pmt.Program] = pmt
						}
					case ps.TableID == 0x42:
						if sdt, err := ts.ParseSDT(ps); err == nil {
							sdtParts[ps.Number] = sdt
							sdtLast = ps.Last
						}
					case ps.TableID == 0x40:
						if nit, err := ts.ParseNIT(ps); err == nil {
							nitParts[ps.Number] = nit
							nitLast = ps.Last
						}
					}
				})
			})
			if complete() {
				log.Printf("scan: mux %s complete in %s", muxID, time.Since(start).Round(time.Millisecond))
				return s.merge(res, sdtParts, nitParts), nil
			}
		}
	}
}

func (s *Scanner) merge(res *result, sdtParts map[byte]*ts.SDT, nitParts map[byte]*ts.NIT) *result {
	for _, p := range sdtParts {
		if res.sdt == nil {
			c := *p
			res.sdt = &c
		} else {
			res.sdt.Services = append(res.sdt.Services, p.Services...)
		}
	}
	for _, p := range nitParts {
		if res.nit == nil {
			c := *p
			res.nit = &c
		} else {
			res.nit.Transports = append(res.nit.Transports, p.Transports...)
		}
	}
	return res
}

// finish stores scan results.
func (s *Scanner) finish(muxID string, res *result, scanErr error) {
	var discovered []string
	s.st.Update(func(state *store.State) error {
		mux, ok := state.Muxes[muxID]
		if !ok {
			return nil
		}
		mux.LastScan = time.Now()
		if scanErr != nil {
			mux.ScanStatus = "fail"
			mux.ScanError = scanErr.Error()
			log.Printf("scan: mux %s failed: %v", muxID, scanErr)
			return nil
		}
		mux.ScanStatus = "ok"
		mux.ScanError = ""
		mux.TSID = res.pat.TSID
		sdtBySID := map[uint16]ts.SDTService{}
		if res.sdt != nil {
			mux.ONID = res.sdt.ONID
			for _, sv := range res.sdt.Services {
				sdtBySID[sv.SID] = sv
			}
		}
		net := state.Networks[mux.NetworkID]
		if res.nit != nil && net != nil {
			net.NetworkID = res.nit.NetworkID
			if mux.ONID == 0 {
				for _, tr := range res.nit.Transports {
					if tr.TSID == mux.TSID {
						mux.ONID = tr.ONID
					}
				}
			}
		}
		now := time.Now()
		for prog := range res.pat.Programs {
			if prog == 0 {
				continue
			}
			id := store.ServiceID(mux.ID, prog)
			svc, exists := state.Services[id]
			if !exists {
				svc = &store.Service{ID: id, MuxID: mux.ID, SID: prog, Enabled: true}
				state.Services[id] = svc
			}
			svc.LastSeen = now
			svc.PMTPID = res.pat.Programs[prog]
			hasVideo, hasAudio := false, false
			if pmt := res.pmts[prog]; pmt != nil {
				svc.PCRPID = pmt.PCRPID
				svc.Scrambled = pmt.Scrambled
				svc.Streams = svc.Streams[:0]
				for _, es := range pmt.Streams {
					svc.Streams = append(svc.Streams, store.Stream{PID: es.PID, StreamType: es.StreamType, Kind: es.Kind, Lang: es.Lang})
					switch es.Kind {
					case "MPEG2VIDEO", "H264", "HEVC":
						hasVideo = true
					case "MPEG2AUDIO", "AAC", "AAC-LATM", "AC3", "EAC3", "AC4":
						hasAudio = true
					}
				}
			}
			if sv, ok := sdtBySID[prog]; ok {
				svc.Name, svc.Provider, svc.Type = sv.Name, sv.Provider, sv.Type
				svc.Kind = ts.ServiceKind(sv.Type)
				if sv.FreeCA {
					svc.Scrambled = true
				}
			}
			if svc.Kind == "" || svc.Kind == "other" {
				switch {
				case hasVideo:
					svc.Kind = "tv"
				case hasAudio:
					svc.Kind = "radio"
				default:
					svc.Kind = "other"
				}
			}
			if svc.Name == "" {
				svc.Name = fmt.Sprintf("Service %d", prog)
			}
		}
		if res.nit == nil || net == nil {
			return nil
		}
		for _, tr := range res.nit.Transports {
			// Logical channel numbers apply to whichever mux carries that TS.
			for _, m := range state.Muxes {
				if m.NetworkID != net.ID || m.TSID != tr.TSID || (m.ONID != 0 && m.ONID != tr.ONID) {
					continue
				}
				for sid, lcn := range tr.LCN {
					if svc, ok := state.Services[store.ServiceID(m.ID, sid)]; ok {
						svc.LCN = lcn
					}
				}
			}
			if !net.DiscoverMuxes || tr.Delivery == nil || tr.Delivery.FrequencyKHz == 0 {
				continue
			}
			if !deliveryMatchesNetwork(tr.Delivery.System, net.Type) {
				continue
			}
			if findMux(state, net.ID, tr.Delivery) != nil {
				continue
			}
			d := tr.Delivery
			m := &store.Mux{ID: store.NewID(), NetworkID: net.ID, Enabled: true, ScanStatus: "new",
				TSID: tr.TSID, ONID: tr.ONID,
				Tuning: store.Tuning{DeliverySystem: d.System, FrequencyKHz: d.FrequencyKHz, BandwidthHz: d.BandwidthHz,
					SymbolRate: d.SymbolRate, Modulation: d.Modulation, FEC: d.FEC, CodeRateLP: d.CodeRateLP,
					GuardInterval: d.GuardInterval, TransmissionMode: d.TransmissionMode, Hierarchy: d.Hierarchy,
					Polarization: d.Polarization, Rolloff: d.Rolloff, StreamID: d.StreamID}}
			state.Muxes[m.ID] = m
			discovered = append(discovered, m.ID)
			log.Printf("scan: discovered mux %s %d kHz via NIT", d.System, d.FrequencyKHz)
		}
		return nil
	})
	if len(discovered) > 0 {
		s.Enqueue(discovered...)
	}
}

func deliveryMatchesNetwork(sys, netType string) bool {
	switch netType {
	case "dvbt":
		return strings.HasPrefix(sys, "DVB-T")
	case "dvbc":
		return strings.HasPrefix(sys, "DVB-C")
	case "dvbs":
		return strings.HasPrefix(sys, "DVB-S")
	}
	return false
}

func findMux(state *store.State, netID string, d *ts.Delivery) *store.Mux {
	for _, m := range state.Muxes {
		if m.NetworkID != netID {
			continue
		}
		diff := int64(m.Tuning.FrequencyKHz) - int64(d.FrequencyKHz)
		if diff < 0 {
			diff = -diff
		}
		tol := int64(1000)
		if strings.HasPrefix(d.System, "DVB-S") {
			tol = 4000
			if m.Tuning.Polarization != d.Polarization {
				continue
			}
		}
		if diff <= tol && (d.StreamID < 0 || m.Tuning.StreamID < 0 || m.Tuning.StreamID == d.StreamID) {
			return m
		}
	}
	return nil
}

// MapOptions controls automatic channel creation.
type MapOptions struct {
	ServiceIDs    []string `json:"serviceIds"` // empty = all enabled services
	IncludeRadio  bool     `json:"includeRadio"`
	SkipScrambled bool     `json:"skipScrambled"`
	MergeByName   bool     `json:"mergeByName"` // same name on another mux becomes a failover service
}

// MapServices creates channels for services that are not mapped yet.
// Returns the number of channels created and services added as failover.
func MapServices(st *store.Store, opt MapOptions) (created, merged int) {
	st.Update(func(state *store.State) error {
		mapped := map[string]bool{}
		used := map[int]bool{}
		byName := map[string]*store.Channel{}
		maxNum := 0
		for _, c := range state.Channels {
			for _, s := range c.Services {
				mapped[s] = true
			}
			used[c.Number] = true
			maxNum = max(maxNum, c.Number)
			byName[strings.ToLower(c.Name)] = c
		}
		var svcs []*store.Service
		if len(opt.ServiceIDs) > 0 {
			for _, id := range opt.ServiceIDs {
				if s, ok := state.Services[id]; ok {
					svcs = append(svcs, s)
				}
			}
		} else {
			for _, s := range state.Services {
				svcs = append(svcs, s)
			}
		}
		sort.Slice(svcs, func(i, j int) bool {
			a, b := svcs[i], svcs[j]
			if (a.LCN == 0) != (b.LCN == 0) {
				return a.LCN != 0
			}
			if a.LCN != b.LCN {
				return a.LCN < b.LCN
			}
			return a.Name < b.Name
		})
		for _, s := range svcs {
			if mapped[s.ID] || !s.Enabled || s.Kind == "other" || (s.Kind == "radio" && !opt.IncludeRadio) ||
				(s.Scrambled && opt.SkipScrambled) {
				continue
			}
			if c := byName[strings.ToLower(s.Name)]; c != nil && opt.MergeByName {
				c.Services = append(c.Services, s.ID)
				mapped[s.ID] = true
				merged++
				continue
			}
			num := s.LCN
			if num <= 0 || used[num] {
				num = maxNum + 1
			}
			used[num] = true
			maxNum = max(maxNum, num)
			c := &store.Channel{ID: store.NewID(), Number: num, Name: s.Name, Enabled: true,
				Services: []string{s.ID}, Radio: s.Kind == "radio"}
			state.Channels[c.ID] = c
			byName[strings.ToLower(c.Name)] = c
			mapped[s.ID] = true
			created++
		}
		return nil
	})
	return
}
