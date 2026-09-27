package tuner

import (
	"sync"
	"sync/atomic"
	"time"

	"dvbhub/internal/store"
	"dvbhub/internal/ts"
)

// Subscription receives transport stream data for one service (filtered,
// with a single-program PAT) or a whole mux (raw).
type Subscription struct {
	ID     int
	Weight int
	req    Request
	m      *Manager

	// C carries packet chunks. It is never closed; select on Done as well.
	C    chan []byte
	Done chan struct{}

	// guarded by m.mu
	sess      *session
	svc       *store.Service
	cur       int
	raw       bool
	muxID     string
	closed    bool
	failovers int
	err       error

	started      time.Time
	once         sync.Once
	bytesOut     atomic.Uint64
	dropped      atomic.Uint64
	failoverBusy atomic.Bool
	transcode    atomic.Value // any: status provided by the output layer

	// filter state, guarded by fmu
	fmu        sync.Mutex
	filterSess *session
	filterSvc  *store.Service
	sid        uint16
	pmtPID     uint16
	pmtSeen    *ts.PMT
	pids       map[uint16]bool
	patCC      byte
	patVer     byte
	nullCC     byte
}

func newSubscription(m *Manager, id int, req Request) *Subscription {
	if req.Weight == 0 {
		req.Weight = WeightLive
	}
	return &Subscription{ID: id, Weight: req.Weight, req: req, m: m,
		C: make(chan []byte, 2048), Done: make(chan struct{}), started: time.Now()}
}

// Close ends the subscription.
func (s *Subscription) Close() { s.end(nil) }

// Err returns why the subscription ended (nil if closed by the owner).
func (s *Subscription) Err() error {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	return s.err
}

// SetTranscodeStatus attaches output-layer status shown in the UI.
func (s *Subscription) SetTranscodeStatus(v any) { s.transcode.Store(v) }

func (s *Subscription) end(err error) {
	s.m.unsubscribe(s)
	s.m.mu.Lock()
	s.closeLocked(err)
	s.m.mu.Unlock()
}

func (s *Subscription) closeLocked(err error) {
	delete(s.m.subs, s.ID)
	s.sess = nil
	if !s.closed {
		s.closed = true
		s.err = err
	}
	s.once.Do(func() { close(s.Done) })
}

func (s *Subscription) push(b []byte) {
	select {
	case s.C <- b:
		s.bytesOut.Add(uint64(len(b)))
	default:
		s.dropped.Add(uint64(len(b) / ts.PacketSize))
	}
}

// ServiceName returns the name of the currently used service.
func (s *Subscription) ServiceName() string {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if s.svc != nil {
		return s.svc.Name
	}
	return ""
}

func (s *Subscription) status() SubStatus {
	st := SubStatus{ID: s.ID, Name: s.req.Name, Client: s.req.Client, Weight: s.Weight, Profile: s.req.Profile,
		Started: s.started, BytesOut: s.bytesOut.Load(), Dropped: s.dropped.Load(), Failovers: s.failovers, State: "ended"}
	if s.svc != nil {
		st.Service = s.svc.Name
	} else if s.raw {
		st.Service = "mux " + s.muxID
	}
	if s.sess != nil {
		s.sess.mu.Lock()
		st.State = s.sess.state
		s.sess.mu.Unlock()
		st.Tuner = s.sess.tuner.src.Key()
	}
	st.Transcode = s.transcode.Load()
	return st
}

// deliver filters one chunk from session sess. Runs on the reader goroutine.
func (s *Subscription) deliver(sess *session, pkts []byte) {
	if s.raw {
		s.push(pkts)
		return
	}
	s.fmu.Lock()
	defer s.fmu.Unlock()
	s.refreshFilter(sess)
	if s.pids == nil {
		return
	}
	out := make([]byte, 0, len(pkts)/2)
	ts.ForEach(pkts, func(p []byte) {
		pid := ts.PID(p)
		if pid == ts.PIDPAT {
			if ts.PUSI(p) {
				out = s.appendPAT(out)
			}
			return
		}
		if s.pids[pid] {
			out = append(out, p...)
		}
	})
	if len(out) > 0 {
		s.push(out)
	}
}

// refreshFilter recomputes the PID set when the session, PAT or PMT changes.
func (s *Subscription) refreshFilter(sess *session) {
	sess.m.mu.Lock()
	svc := s.svc
	sess.m.mu.Unlock()
	if svc == nil {
		s.pids = nil
		return
	}
	changed := false
	if s.filterSess != sess || s.filterSvc != svc {
		s.filterSess, s.filterSvc = sess, svc
		s.sid = svc.SID
		s.pmtPID = svc.PMTPID
		s.pmtSeen = nil
		s.patVer = (s.patVer + 1) & 0x1f
		changed = true
	}
	if pid, ok := sess.pmtPIDs[s.sid]; ok && pid != s.pmtPID {
		s.pmtPID = pid
		s.patVer = (s.patVer + 1) & 0x1f
		changed = true
	}
	if p := sess.pmts[s.sid]; p != nil && p != s.pmtSeen {
		s.pmtSeen = p
		changed = true
	}
	if !changed && s.pids != nil {
		return
	}
	pids := map[uint16]bool{s.pmtPID: true}
	if p := s.pmtSeen; p != nil {
		pids[p.PCRPID] = true
		for _, es := range p.Streams {
			if es.Kind != "" {
				pids[es.PID] = true
			}
		}
	} else {
		// Until the live PMT arrives use what the last scan found.
		pids[svc.PCRPID] = true
		for _, st := range svc.Streams {
			if st.Kind != "" {
				pids[st.PID] = true
			}
		}
	}
	delete(pids, ts.PIDNull)
	s.pids = pids
}

func (s *Subscription) appendPAT(out []byte) []byte {
	if s.pmtPID == 0 {
		return out
	}
	sec := ts.BuildPAT(1, s.patVer, map[uint16]uint16{s.sid: s.pmtPID})
	return append(out, ts.Packetize(ts.PIDPAT, sec, &s.patCC)...)
}

var nullPacket = func() []byte {
	p := make([]byte, ts.PacketSize)
	p[0], p[1], p[2], p[3] = ts.SyncByte, 0x1f, 0xff, 0x10
	for i := 4; i < len(p); i++ {
		p[i] = 0xff
	}
	return p
}()

// keepalive emits a PAT and null packets so clients keep the connection
// open (and their read timeouts reset) while the tuner has no signal.
func (s *Subscription) keepalive(sess *session) {
	if s.raw {
		return
	}
	s.fmu.Lock()
	defer s.fmu.Unlock()
	s.refreshFilter(sess)
	out := s.appendPAT(nil)
	for i := 0; i < 20; i++ {
		out = append(out, nullPacket...)
	}
	s.push(out)
}

// NullPackets returns n null TS packets (used by the output layer).
func NullPackets(n int) []byte {
	out := make([]byte, 0, n*ts.PacketSize)
	for i := 0; i < n; i++ {
		out = append(out, nullPacket...)
	}
	return out
}
