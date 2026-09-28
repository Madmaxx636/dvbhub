package tuner

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"dvbhub/internal/dvb"
	"dvbhub/internal/store"
	"dvbhub/internal/ts"
)

var errNoData = errors.New("no data from tuner (signal lost?)")

// MuxLabel describes a mux for logs and the UI.
func MuxLabel(m store.Mux) string {
	if m.File != "" {
		return "file " + filepath.Base(m.File)
	}
	return dvb.DescribeTuning(m.Tuning)
}

// session is one tuner tuned to one mux, shared by all its subscribers.
type session struct {
	m     *Manager
	tuner *Tuner
	mux   store.Mux
	net   store.Network
	prev  *session // previous session on the same tuner; we wait for it to release the device
	done  chan struct{}

	// guarded by m.mu
	subs    map[*Subscription]struct{}
	stopped bool
	linger  *time.Timer

	ctx    context.Context
	cancel context.CancelFunc

	// guarded by mu
	mu           sync.Mutex
	subList      []*Subscription
	state        string
	lastErr      error
	retunes      int
	tuneFailures int
	outageStart  time.Time
	everData     bool
	stream       Stream
	kbps         float64
	ccErrors     uint64
	teiErrors    uint64
	lastSample   time.Time
	sampleBytes  uint64
	sampleCC     uint64

	bytes  atomic.Uint64
	ccErr  atomic.Uint64
	teiErr atomic.Uint64

	// reader goroutine only
	patAsm  *ts.SectionAssembler
	pmtAsm  map[uint16]*ts.SectionAssembler // by pid
	tsid    uint16
	pmtPIDs map[uint16]uint16 // sid -> pmt pid
	pmts    map[uint16]*ts.PMT
	lastCC  [8192]int8
}

func newSession(m *Manager, t *Tuner, mux store.Mux, net store.Network) *session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{m: m, tuner: t, mux: mux, net: net, prev: t.lastSess, done: make(chan struct{}),
		subs: map[*Subscription]struct{}{}, ctx: ctx, cancel: cancel, state: "tuning",
		patAsm: ts.NewSectionAssembler(), pmtAsm: map[uint16]*ts.SectionAssembler{},
		pmtPIDs: map[uint16]uint16{}, pmts: map[uint16]*ts.PMT{}}
	for i := range s.lastCC {
		s.lastCC[i] = -1
	}
	t.lastSess = s
	return s
}

func (s *session) addLocked(sub *Subscription, svc *store.Service) {
	s.subs[sub] = struct{}{}
	sub.sess = s
	sub.svc = svc
	if s.linger != nil {
		s.linger.Stop()
		s.linger = nil
	}
	s.syncSubList()
}

func (s *session) removeLocked(sub *Subscription) {
	delete(s.subs, sub)
	if sub.sess == s {
		sub.sess = nil
	}
	s.syncSubList()
	if len(s.subs) == 0 && !s.stopped && s.linger == nil {
		s.linger = time.AfterFunc(lingerTime, func() {
			s.m.mu.Lock()
			defer s.m.mu.Unlock()
			if len(s.subs) == 0 && !s.stopped {
				s.stopLocked(nil)
			}
		})
	}
}

func (s *session) syncSubList() {
	list := make([]*Subscription, 0, len(s.subs))
	for sub := range s.subs {
		list = append(list, sub)
	}
	s.mu.Lock()
	s.subList = list
	s.mu.Unlock()
}

func (s *session) maxWeightLocked() int {
	w := 0
	for sub := range s.subs {
		if sub.Weight > w {
			w = sub.Weight
		}
	}
	return w
}

// stopLocked ends the session and all its subscriptions. Caller holds m.mu.
func (s *session) stopLocked(err error) {
	if s.stopped {
		return
	}
	s.stopped = true
	s.cancel()
	if s.linger != nil {
		s.linger.Stop()
	}
	if s.m.sessions[s.mux.ID] == s {
		delete(s.m.sessions, s.mux.ID)
	}
	if s.tuner.sess == s {
		s.tuner.sess = nil
	}
	if err == nil {
		err = errors.New("session stopped")
	}
	for sub := range s.subs {
		sub.closeLocked(err)
	}
	s.subs = map[*Subscription]struct{}{}
	s.syncSubList()
}

func (s *session) subscribers() []*Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subList
}

func (s *session) setState(state string, err error) {
	s.mu.Lock()
	s.state = state
	if err != nil {
		s.lastErr = err
	}
	s.mu.Unlock()
}

func (s *session) outage(err error) {
	s.mu.Lock()
	if s.outageStart.IsZero() {
		s.outageStart = time.Now()
		if s.everData {
			log.Printf("tuner %s: signal lost on %s: %v", s.tuner.src.Key(), MuxLabel(s.mux), err)
		}
	}
	s.state = "nosignal"
	s.lastErr = err
	s.mu.Unlock()
}

func (s *session) gotData() {
	s.mu.Lock()
	if s.state != "streaming" {
		if !s.outageStart.IsZero() && s.everData {
			log.Printf("tuner %s: signal recovered after %s", s.tuner.src.Key(), time.Since(s.outageStart).Round(100*time.Millisecond))
		}
		s.state = "streaming"
		s.outageStart = time.Time{}
		s.tuneFailures = 0
		s.everData = true
	}
	s.mu.Unlock()
}

func (s *session) closeStream() {
	s.mu.Lock()
	st := s.stream
	s.stream = nil
	s.mu.Unlock()
	if st != nil {
		st.Close()
	}
}

func (s *session) satInput() *store.SatInput {
	if c, ok := s.tuner.cfg.Sat[s.net.ID]; ok {
		return &c
	}
	return nil
}

func (s *session) run() {
	defer close(s.done)
	defer s.m.sessionEnded(s)
	defer s.closeStream()
	if s.prev != nil {
		<-s.prev.done
	}
	timeout := time.Duration(s.tuner.cfg.TuneTimeout) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	buf := make([]byte, ts.PacketSize*348)
	var al ts.Aligner
	backoff := time.Second
	lastData := time.Now()
	lastKeepalive := time.Time{}

	for s.ctx.Err() == nil {
		s.mu.Lock()
		stream := s.stream
		s.mu.Unlock()
		if stream == nil {
			s.setState("tuning", nil)
			st, err := s.open(timeout)
			if s.ctx.Err() != nil {
				if st != nil {
					st.Close()
				}
				return
			}
			if err != nil {
				s.mu.Lock()
				s.tuneFailures++
				s.mu.Unlock()
				s.outage(err)
				s.keepaliveFor(backoff)
				backoff = min(backoff*2, 10*time.Second)
				continue
			}
			s.mu.Lock()
			s.stream = st
			s.mu.Unlock()
			stream = st
			lastData = time.Now()
			al = ts.Aligner{}
		}
		stream.SetDeadline(time.Now().Add(700 * time.Millisecond))
		n, err := stream.Read(buf)
		if n > 0 {
			if pkts := al.Feed(buf[:n]); len(pkts) > 0 {
				lastData = time.Now()
				backoff = time.Second
				s.gotData()
				s.process(pkts)
			}
		}
		if err != nil && s.ctx.Err() == nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			log.Printf("tuner %s: read error: %v", s.tuner.src.Key(), err)
			s.closeStream()
			s.outage(err)
			s.keepaliveFor(time.Second)
			continue
		}
		if since := time.Since(lastData); since > 1500*time.Millisecond {
			s.outage(errNoData)
			if time.Since(lastKeepalive) >= 500*time.Millisecond {
				s.keepalive()
				lastKeepalive = time.Now()
			}
			if since > 6*time.Second {
				// The frontend did not recover on its own: re-tune from scratch.
				s.closeStream()
				s.mu.Lock()
				s.retunes++
				s.mu.Unlock()
				lastData = time.Now()
			}
		}
	}
}

// open tunes the tuner, sending keep-alives every 500 ms while it works so
// viewers stay connected (and failover can trigger) during slow tunes, such
// as tuners that load firmware when opened.
func (s *session) open(timeout time.Duration) (Stream, error) {
	type result struct {
		st  Stream
		err error
	}
	ch := make(chan result, 1)
	go func() {
		st, err := s.tuner.src.Open(s.ctx, s.mux, s.satInput(), timeout)
		ch <- result{st, err}
	}()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case r := <-ch:
			return r.st, r.err
		case <-tick.C:
			s.keepalive()
		}
	}
}

// keepaliveFor sends keep-alive packets for d (or until the session stops).
func (s *session) keepaliveFor(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		s.keepalive()
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// keepalive keeps subscribers' connections open during an outage and
// triggers failover to alternative services when the outage persists.
func (s *session) keepalive() {
	s.mu.Lock()
	outage := time.Duration(0)
	if !s.outageStart.IsZero() {
		outage = time.Since(s.outageStart)
	}
	neverStreamed := !s.everData && s.tuneFailures > 0
	s.mu.Unlock()
	if outage > maxOutage {
		log.Printf("tuner %s: no signal on %s for %s, ending session", s.tuner.src.Key(), MuxLabel(s.mux), outage.Round(time.Second))
		s.m.mu.Lock()
		s.stopLocked(errors.New("no signal"))
		s.m.mu.Unlock()
		return
	}
	for _, sub := range s.subscribers() {
		sub.keepalive(s)
		if !sub.raw && len(sub.req.Services) > 1 && (outage > failoverAfter || neverStreamed) &&
			sub.failoverBusy.CompareAndSwap(false, true) {
			go func(sub *Subscription) {
				defer sub.failoverBusy.Store(false)
				s.m.failover(sub)
			}(sub)
		}
	}
}

func (s *session) process(pkts []byte) {
	s.bytes.Add(uint64(len(pkts)))
	ts.ForEach(pkts, func(p []byte) {
		pid := ts.PID(p)
		if ts.TEI(p) {
			s.teiErr.Add(1)
			return
		}
		if pid != ts.PIDNull && ts.HasPayload(p) {
			cc := int8(ts.CC(p))
			if last := s.lastCC[pid]; last >= 0 && cc != last && cc != (last+1)&0x0f && !discontinuity(p) {
				s.ccErr.Add(1)
			}
			s.lastCC[pid] = cc
		}
		switch {
		case pid == ts.PIDPAT:
			s.patAsm.Push(p, s.onPAT)
		default:
			if a := s.pmtAsm[pid]; a != nil {
				a.Push(p, s.onPMT)
			}
		}
	})
	s.m.mu.Lock()
	taps := s.m.taps
	s.m.mu.Unlock()
	for _, tap := range taps {
		tap(s.mux.ID, pkts)
	}
	for _, sub := range s.subscribers() {
		sub.deliver(s, pkts)
	}
}

func discontinuity(p []byte) bool {
	return ts.HasAdaptation(p) && p[4] > 0 && p[5]&0x80 != 0
}

func (s *session) onPAT(sec []byte) {
	ps, err := ts.ParseSection(sec)
	if err != nil {
		return
	}
	pat, err := ts.ParsePAT(ps)
	if err != nil {
		return
	}
	s.tsid = pat.TSID
	for prog, pid := range pat.Programs {
		if prog == 0 {
			continue
		}
		s.pmtPIDs[prog] = pid
		if s.pmtAsm[pid] == nil {
			s.pmtAsm[pid] = ts.NewSectionAssembler()
		}
	}
}

func (s *session) onPMT(sec []byte) {
	ps, err := ts.ParseSection(sec)
	if err != nil || ps.TableID != 0x02 {
		return
	}
	pmt, err := ts.ParsePMT(ps)
	if err != nil {
		return
	}
	if old := s.pmts[pmt.Program]; old == nil || old.Version != pmt.Version {
		s.pmts[pmt.Program] = pmt
	}
}

// sample returns signal, bitrate and new CC errors since the previous call.
func (s *session) sample() (dvb.Signal, float64, uint64) {
	s.mu.Lock()
	st := s.stream
	now := time.Now()
	b := s.bytes.Load()
	cc := s.ccErr.Load()
	if !s.lastSample.IsZero() {
		el := now.Sub(s.lastSample).Seconds()
		if el > 0 {
			s.kbps = float64(b-s.sampleBytes) * 8 / 1000 / el
		}
	}
	dcc := cc - s.sampleCC
	s.lastSample, s.sampleBytes, s.sampleCC = now, b, cc
	s.ccErrors = cc
	s.teiErrors = s.teiErr.Load()
	kbps := s.kbps
	s.mu.Unlock()
	sig := dvb.Signal{StrengthPct: -1, SNRPct: -1, BER: -1, Updated: now}
	if st != nil {
		sig = st.Signal()
	}
	return sig, kbps, dcc
}
