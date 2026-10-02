// Package scan tunes muxes, reads their PSI/SI (PAT, PMT, SDT, NIT) or ATSC
// PSIP (VCT) tables and stores the services found. It also creates muxes from
// the built-in channel plans and maps services to channels.
package scan

import (
	"errors"
	"log"
	"slices"
	"sort"
	"sync"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/dvb"
	"dvbhub/internal/tuners"
)

// Job tracks a scan started for a whole network.
type Job struct {
	NetworkID string     `json:"networkId"`
	Network   string     `json:"network"`
	Total     int        `json:"total"`
	Done      int        `json:"done"`
	Locked    int        `json:"locked"`   // muxes that delivered data
	Services  int        `json:"services"` // services found so far
	Current   []string   `json:"current"`  // labels of muxes being scanned now
	Started   time.Time  `json:"started"`
	Finished  *time.Time `json:"finished,omitempty"`
	AutoMap   bool       `json:"autoMap"`
	Mapped    int        `json:"mapped"` // channels created by the automatic mapping
	Cancelled bool       `json:"cancelled,omitempty"`

	pending map[string]bool
	mapOpt  MapOptions
}

// Status is the scanner state shown in the UI.
type Status struct {
	Queued []string `json:"queued"`
	Active []string `json:"active"`
	Jobs   []Job    `json:"jobs"`
}

type Scanner struct {
	st *config.Store
	tm *tuners.Manager

	mu     sync.Mutex
	queue  []string
	active map[string]*tuners.Subscription
	jobs   map[string]*Job // by network id
	wake   chan struct{}
}

// New starts the scanner. Muxes left queued or scanning by a previous run are
// scanned again.
func New(st *config.Store, tm *tuners.Manager) *Scanner {
	s := &Scanner{st: st, tm: tm, active: map[string]*tuners.Subscription{}, jobs: map[string]*Job{},
		wake: make(chan struct{}, 1)}
	var pending []string
	st.View(func(state *config.State) {
		for id, m := range state.Muxes {
			if m.Scan.Status == "queued" || m.Scan.Status == "scanning" {
				pending = append(pending, id)
			}
		}
	})
	sort.Strings(pending)
	s.Enqueue(pending...)
	go s.loop()
	return s
}

// Enqueue schedules muxes for scanning.
func (s *Scanner) Enqueue(ids ...string) {
	if len(ids) == 0 {
		return
	}
	s.st.Update(func(state *config.State) error {
		for _, id := range ids {
			if m, ok := state.Muxes[id]; ok {
				m.Scan = config.ScanState{Status: "queued", At: m.Scan.At}
			}
		}
		return nil
	})
	s.mu.Lock()
	for _, id := range ids {
		if !slices.Contains(s.queue, id) && s.active[id] == nil {
			s.queue = append(s.queue, id)
		}
	}
	s.mu.Unlock()
	s.kick()
}

func (s *Scanner) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ScanNetwork queues every enabled mux of a network as one job. With autoMap,
// services found are mapped to channels when the job finishes.
func (s *Scanner) ScanNetwork(netID string, autoMap bool, opt MapOptions) (int, error) {
	var ids []string
	var name string
	s.st.View(func(state *config.State) {
		if n, ok := state.Networks[netID]; ok {
			name = n.Name
		}
		for id, m := range state.Muxes {
			if m.NetworkID == netID && m.Enabled {
				ids = append(ids, id)
			}
		}
	})
	if name == "" {
		return 0, errors.New("network not found")
	}
	if len(ids) == 0 {
		return 0, errors.New("this network has no muxes to scan yet")
	}
	sort.Slice(ids, func(i, j int) bool { return s.muxOrder(ids[i]) < s.muxOrder(ids[j]) })
	job := &Job{NetworkID: netID, Network: name, Total: len(ids), Started: time.Now(), AutoMap: autoMap,
		pending: map[string]bool{}, mapOpt: opt, Current: []string{}}
	for _, id := range ids {
		job.pending[id] = true
	}
	s.mu.Lock()
	s.jobs[netID] = job
	s.mu.Unlock()
	s.Enqueue(ids...)
	return len(ids), nil
}

func (s *Scanner) muxOrder(id string) uint32 {
	var f uint32
	s.st.View(func(state *config.State) {
		if m, ok := state.Muxes[id]; ok {
			f = m.Tuning.FrequencyKHz
		}
	})
	return f
}

// Cancel drops queued muxes and stops running scans (of one network, or all
// when netID is empty).
func (s *Scanner) Cancel(netID string) {
	inNet := func(muxID string) bool {
		if netID == "" {
			return true
		}
		ok := false
		s.st.View(func(state *config.State) {
			if m, found := state.Muxes[muxID]; found {
				ok = m.NetworkID == netID
			}
		})
		return ok
	}
	s.mu.Lock()
	var dropped []string
	keep := s.queue[:0]
	for _, id := range s.queue {
		if inNet(id) {
			dropped = append(dropped, id)
		} else {
			keep = append(keep, id)
		}
	}
	s.queue = keep
	var running []*tuners.Subscription
	for id, sub := range s.active {
		if inNet(id) {
			running = append(running, sub)
		}
	}
	for id, j := range s.jobs {
		if (netID == "" || id == netID) && j.Finished == nil {
			j.Cancelled = true
			j.AutoMap = false
		}
	}
	s.mu.Unlock()
	for _, sub := range running {
		sub.Close()
	}
	s.st.Update(func(state *config.State) error {
		for _, id := range dropped {
			if m, ok := state.Muxes[id]; ok && m.Scan.Status == "queued" {
				m.Scan.Status = "new"
			}
		}
		return nil
	})
	for _, id := range dropped {
		s.jobDone(id, "", 0)
	}
}

// Status returns queued and active muxes and network jobs.
func (s *Scanner) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Queued: append([]string{}, s.queue...), Active: []string{}, Jobs: []Job{}}
	for id := range s.active {
		st.Active = append(st.Active, id)
	}
	sort.Strings(st.Active)
	for _, j := range s.jobs {
		c := *j
		c.Current = append([]string{}, j.Current...)
		st.Jobs = append(st.Jobs, c)
	}
	sort.Slice(st.Jobs, func(i, j int) bool { return st.Jobs[i].Started.After(st.Jobs[j].Started) })
	return st
}

// Busy reports whether anything is queued or being scanned.
func (s *Scanner) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue) > 0 || len(s.active) > 0
}

func (s *Scanner) loop() {
	for {
		s.mu.Lock()
		var remaining []string
		for _, id := range s.queue {
			sub, err := s.tm.Subscribe(tuners.Request{MuxID: id, Weight: tuners.WeightScan, Name: "scan"})
			if errors.Is(err, tuners.ErrNoTuner) {
				if cerr := s.tm.CanTune(id); cerr != nil {
					err = cerr // no tuner could ever receive it: fail instead of waiting
				} else {
					remaining = append(remaining, id)
					continue
				}
			}
			if err != nil {
				go func(id string, err error) {
					s.finish(id, nil, err, nil)
					s.jobDone(id, "fail", 0)
				}(id, err)
				continue
			}
			s.active[id] = sub
			s.setCurrent(id, true)
			go s.run(id, sub)
		}
		s.queue = remaining
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-time.After(2 * time.Second):
		}
	}
}

// setCurrent updates the "scanning now" list of the mux's job. Caller holds s.mu.
func (s *Scanner) setCurrent(muxID string, on bool) {
	var label, netID string
	s.st.View(func(state *config.State) {
		if m, ok := state.Muxes[muxID]; ok {
			label, netID = tuners.MuxLabel(*m), m.NetworkID
		}
	})
	j := s.jobs[netID]
	if j == nil || !j.pending[muxID] {
		return
	}
	j.Current = slices.DeleteFunc(j.Current, func(x string) bool { return x == label })
	if on {
		j.Current = append(j.Current, label)
	}
}

func (s *Scanner) run(id string, sub *tuners.Subscription) {
	res, err := s.scanMux(id, sub)
	var snap *dvb.SignalSnap
	if sig, ok := s.tm.MuxSignal(id); ok && !sig.Updated.IsZero() && (sig.Locked || sig.StrengthPct >= 0) {
		sn := sig.Snapshot()
		snap = &sn
	}
	sub.Close()
	found := s.finish(id, res, err, snap)
	s.mu.Lock()
	s.setCurrent(id, false)
	delete(s.active, id)
	s.mu.Unlock()
	s.jobDone(id, statusOf(res, err), found)
	s.kick()
}

// jobDone records a finished mux in its network job and runs the automatic
// mapping when the job is complete.
func (s *Scanner) jobDone(muxID, status string, found int) {
	s.mu.Lock()
	var job *Job
	for _, j := range s.jobs {
		if j.pending[muxID] {
			job = j
		}
	}
	if job == nil {
		s.mu.Unlock()
		return
	}
	delete(job.pending, muxID)
	job.Done++
	job.Services += found
	if status == "ok" {
		job.Locked++
	}
	finished := len(job.pending) == 0
	if finished {
		now := time.Now()
		job.Finished = &now
		job.Current = []string{}
	}
	autoMap, opt, netID := job.AutoMap, job.mapOpt, job.NetworkID
	s.mu.Unlock()
	if !finished {
		return
	}
	log.Printf("scan: network %s finished", netID)
	if autoMap {
		created, merged := MapServices(s.st, opt)
		log.Printf("scan: mapped %d new channels (%d added as backups)", created, merged)
		s.mu.Lock()
		job.Mapped = created
		s.mu.Unlock()
	}
}

func statusOf(res *result, err error) string {
	switch {
	case err == nil && res != nil:
		return "ok"
	case errors.Is(err, errNoSignal):
		return "nosignal"
	}
	return "fail"
}

func (s *Scanner) muxName(id string) string {
	name := id
	s.st.View(func(state *config.State) {
		if m, ok := state.Muxes[id]; ok {
			name = tuners.MuxLabel(*m)
		}
	})
	return name
}
