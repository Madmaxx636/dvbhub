// Package tuner allocates tuners to subscriptions, runs one reader per tuned
// mux, fans the transport stream out to subscribers and keeps subscriptions
// alive across signal loss by re-tuning and failing over.
package tuner

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"dvbhub/internal/dvb"
	"dvbhub/internal/store"
)

const (
	WeightEPG  = 5
	WeightScan = 10
	WeightLive = 100
	WeightDVR  = 500

	historyLen    = 300 // one sample per second
	lingerTime    = 4 * time.Second
	failoverAfter = 8 * time.Second
	maxOutage     = 60 * time.Second // then the session ends and frees the tuner
)

var (
	ErrNoTuner   = errors.New("no free tuner")
	ErrPreempted = errors.New("tuner taken by a higher priority subscription")
	ErrNoService = errors.New("no usable service")
)

// Sample is one point of a tuner's signal history.
type Sample struct {
	T        int64   `json:"t"`
	Locked   bool    `json:"locked"`
	Strength float64 `json:"strength"`
	SNR      float64 `json:"snr"` // dB if known, else percent
	SNRIsDB  bool    `json:"snrIsDb"`
	BER      float64 `json:"ber"`
	UNC      uint64  `json:"unc"`
	CCErr    uint64  `json:"ccErr"`
	Kbps     float64 `json:"kbps"`
}

// Tuner is a Source plus its configuration and runtime state.
type Tuner struct {
	src      Source
	cfg      store.TunerConfig
	sess     *session
	lastSess *session
	signal   dvb.Signal
	history  []Sample
}

type Manager struct {
	st *store.Store

	mu       sync.Mutex
	tuners   []*Tuner
	sessions map[string]*session // by mux id
	subs     map[int]*Subscription
	nextSub  int
	taps     []func(muxID string, pkts []byte)
	hw       []dvb.FrontendInfo
}

func NewManager(st *store.Store) *Manager {
	m := &Manager{st: st, sessions: map[string]*session{}, subs: map[int]*Subscription{}}
	m.hw = dvb.Discover()
	for _, h := range m.hw {
		log.Printf("tuner: found %s %q (%s)", h.Key, h.Name, strings.Join(h.DelSys, ", "))
	}
	m.Reload()
	st.OnChange(m.Reload)
	go m.monitor()
	return m
}

// Rediscover rescans /dev/dvb for hardware.
func (m *Manager) Rediscover() {
	hw := dvb.Discover()
	m.mu.Lock()
	m.hw = hw
	m.mu.Unlock()
	m.Reload()
}

// Reload syncs tuner objects and their config with the store.
func (m *Manager) Reload() {
	var virtualN int
	m.st.View(func(st *store.State) { virtualN = st.Settings.VirtualTuners })

	m.mu.Lock()
	var srcs []Source
	for _, h := range m.hw {
		srcs = append(srcs, &hwSource{info: h})
	}
	for i := 0; i < virtualN; i++ {
		srcs = append(srcs, NewVirtualSource(i))
	}
	existing := map[string]*Tuner{}
	for _, t := range m.tuners {
		existing[t.src.Key()] = t
	}
	var tuners []*Tuner
	var missing []store.TunerConfig
	m.st.View(func(st *store.State) {
		for _, s := range srcs {
			t := existing[s.Key()]
			if t == nil {
				t = &Tuner{src: s}
			}
			if c, ok := st.Tuners[s.Key()]; ok {
				t.cfg = *c
			} else {
				tt := 15 // hardware: allow for firmware loading on first open
				if s.Virtual() {
					tt = 5
				}
				t.cfg = store.TunerConfig{Key: s.Key(), Name: s.Name(), Enabled: true, TuneTimeout: tt}
				missing = append(missing, t.cfg)
			}
			tuners = append(tuners, t)
		}
	})
	// Tuners that disappeared lose their sessions.
	for key, t := range existing {
		if !slices.ContainsFunc(tuners, func(x *Tuner) bool { return x.src.Key() == key }) && t.sess != nil {
			t.sess.stopLocked(errors.New("tuner removed"))
		}
	}
	m.tuners = tuners
	holds := map[*hwSource]bool{}
	for _, t := range tuners {
		if hw, ok := t.src.(*hwSource); ok {
			holds[hw] = t.cfg.Hold && t.cfg.Enabled
		}
	}
	m.mu.Unlock()
	for hw, on := range holds {
		hw.SetHold(on)
	}
	if len(missing) > 0 {
		m.st.Update(func(st *store.State) error {
			for _, c := range missing {
				c := c
				if _, ok := st.Tuners[c.Key]; !ok {
					st.Tuners[c.Key] = &c
				}
			}
			return nil
		})
	}
}

// AddTap registers fn to receive every packet chunk read from any mux.
// fn runs on the reader goroutine and must be fast.
func (m *Manager) AddTap(fn func(muxID string, pkts []byte)) {
	m.mu.Lock()
	m.taps = append(m.taps, fn)
	m.mu.Unlock()
}

// Request describes what to subscribe to.
type Request struct {
	Services []string // service ids in failover order
	MuxID    string   // raw full-mux subscription (scan, EPG)
	Tuner    string   // only use this tuner (alignment); empty = any
	Weight   int
	Name     string
	Client   string
	Profile  string
}

// Subscribe allocates a tuner and starts delivering packets on sub.C.
// It fails only when no tuner can be allocated; tuning problems are handled
// by retrying in the background while sending keep-alive packets.
func (m *Manager) Subscribe(req Request) (*Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextSub++
	sub := newSubscription(m, m.nextSub, req)
	if req.MuxID != "" {
		sub.raw = true
		sub.muxID = req.MuxID
		if err := m.attachLocked(sub, req.MuxID, nil); err != nil {
			return nil, err
		}
	} else {
		var lastErr error = ErrNoService
		ok := false
		for i := range req.Services {
			sub.cur = i
			svc, err := m.serviceLocked(req.Services[i])
			if err != nil {
				lastErr = err
				continue
			}
			if err := m.attachLocked(sub, svc.MuxID, svc); err != nil {
				lastErr = err
				continue
			}
			ok = true
			break
		}
		if !ok {
			return nil, lastErr
		}
	}
	m.subs[sub.ID] = sub
	return sub, nil
}

func (m *Manager) serviceLocked(id string) (*store.Service, error) {
	var svc *store.Service
	m.st.View(func(st *store.State) {
		if s, ok := st.Services[id]; ok {
			c := *s
			svc = &c
		}
	})
	if svc == nil {
		return nil, fmt.Errorf("service %s not found", id)
	}
	return svc, nil
}

// attachLocked adds sub to a session for muxID, creating one if needed.
func (m *Manager) attachLocked(sub *Subscription, muxID string, svc *store.Service) error {
	return m.attachAvoidLocked(sub, muxID, svc, nil)
}

func (m *Manager) attachAvoidLocked(sub *Subscription, muxID string, svc *store.Service, avoid *Tuner) error {
	if s := m.sessions[muxID]; s != nil && !s.stopped && (sub.req.Tuner == "" || s.tuner.src.Key() == sub.req.Tuner) {
		s.addLocked(sub, svc)
		return nil
	}
	var mux store.Mux
	var net store.Network
	found := false
	m.st.View(func(st *store.State) {
		if x, ok := st.Muxes[muxID]; ok {
			mux = *x
			if n, ok := st.Networks[x.NetworkID]; ok {
				net = *n
				found = true
			}
		}
	})
	if !found {
		return fmt.Errorf("mux %s not found", muxID)
	}
	t := m.pickTunerLocked(mux, net, sub.Weight, avoid, sub.req.Tuner)
	if t == nil {
		return ErrNoTuner
	}
	if t.sess != nil {
		t.sess.stopLocked(ErrPreempted)
	}
	s := newSession(m, t, mux, net)
	m.sessions[muxID] = s
	t.sess = s
	s.addLocked(sub, svc)
	go s.run()
	return nil
}

func compatible(t *Tuner, mux store.Mux, net store.Network) bool {
	if !t.cfg.Enabled {
		return false
	}
	if t.src.Virtual() != (net.Type == "virtual") {
		return false
	}
	if len(t.cfg.Networks) > 0 && !slices.Contains(t.cfg.Networks, net.ID) {
		return false
	}
	if t.src.Virtual() || len(t.src.DelSys()) == 0 {
		return true
	}
	return slices.Contains(t.src.DelSys(), mux.Tuning.DeliverySystem) ||
		(mux.Tuning.DeliverySystem == "DVB-C" && slices.Contains(t.src.DelSys(), "DVB-C/C"))
}

// pickTunerLocked chooses an idle tuner, or one whose users all have lower
// weight. The avoid tuner (one that just lost signal) is used only as a last resort.
func (m *Manager) pickTunerLocked(mux store.Mux, net store.Network, weight int, avoid *Tuner, only string) *Tuner {
	var cands []*Tuner
	for _, t := range m.tuners {
		if compatible(t, mux, net) && (only == "" || t.src.Key() == only) {
			cands = append(cands, t)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if (cands[i] == avoid) != (cands[j] == avoid) {
			return cands[j] == avoid
		}
		return cands[i].cfg.Priority > cands[j].cfg.Priority
	})
	for _, t := range cands {
		if t.sess == nil || t.sess.stopped || len(t.sess.subs) == 0 {
			return t
		}
	}
	var best *Tuner
	bestW := weight
	for _, t := range cands {
		if w := t.sess.maxWeightLocked(); w < bestW {
			best, bestW = t, w
		}
	}
	return best
}

// failover moves sub to its next candidate service. Returns true on success.
func (m *Manager) failover(sub *Subscription) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sub.closed || sub.raw || len(sub.req.Services) < 2 {
		return false
	}
	old := sub.sess
	for step := 1; step < len(sub.req.Services); step++ {
		i := (sub.cur + step) % len(sub.req.Services)
		svc, err := m.serviceLocked(sub.req.Services[i])
		if err != nil || (old != nil && svc.MuxID == old.mux.ID) {
			continue
		}
		var avoid *Tuner
		if old != nil {
			avoid = old.tuner
			old.removeLocked(sub)
		}
		if err := m.attachAvoidLocked(sub, svc.MuxID, svc, avoid); err != nil {
			if old != nil && !old.stopped {
				old.addLocked(sub, sub.svc)
			}
			continue
		}
		sub.cur = i
		sub.failovers++
		log.Printf("tuner: %q failed over to %s on %s via %s", sub.req.Name, svc.Name,
			MuxLabel(sub.sess.mux), sub.sess.tuner.src.Key())
		return true
	}
	return false
}

func (m *Manager) unsubscribe(sub *Subscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.subs, sub.ID)
	if sub.sess != nil {
		sub.sess.removeLocked(sub)
	}
}

func (m *Manager) sessionEnded(s *session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.mux.ID] == s {
		delete(m.sessions, s.mux.ID)
	}
	if s.tuner.sess == s {
		// Remember the last reading so muxes and channels keep showing signal bars.
		if sig := s.tuner.signal; !sig.Updated.IsZero() {
			m.saveSignal(s.mux.ID, sig)
		}
		s.tuner.sess = nil
		s.tuner.signal = dvb.Signal{}
	}
}

// saveSignal stores a signal snapshot on a mux (asynchronously).
func (m *Manager) saveSignal(muxID string, sig dvb.Signal) {
	snap := sig.Snapshot()
	go m.st.Update(func(st *store.State) error {
		if mx, ok := st.Muxes[muxID]; ok {
			mx.Signal = &snap
		}
		return nil
	})
}

// MuxSignal reads the current signal of the tuner receiving muxID, if any.
func (m *Manager) MuxSignal(muxID string) (dvb.Signal, bool) {
	m.mu.Lock()
	s := m.sessions[muxID]
	m.mu.Unlock()
	if s == nil {
		return dvb.Signal{}, false
	}
	s.mu.Lock()
	st := s.stream
	s.mu.Unlock()
	if st == nil {
		return dvb.Signal{StrengthPct: -1, SNRPct: -1, BER: -1, Updated: time.Now()}, true
	}
	return st.Signal(), true
}

// LiveMuxSignals returns the latest once-a-second reading for every tuned mux.
func (m *Manager) LiveMuxSignals() map[string]dvb.Signal {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]dvb.Signal{}
	for _, t := range m.tuners {
		if t.sess != nil && !t.signal.Updated.IsZero() {
			out[t.sess.mux.ID] = t.signal
		}
	}
	return out
}

// TunerSignal reads a tuner's signal right now (for antenna alignment).
func (m *Manager) TunerSignal(key string) (sig dvb.Signal, state, mux string, ok bool) {
	m.mu.Lock()
	var s *session
	for _, t := range m.tuners {
		if t.src.Key() == key {
			ok, s = true, t.sess
		}
	}
	m.mu.Unlock()
	if s == nil {
		return dvb.Signal{StrengthPct: -1, SNRPct: -1, BER: -1}, "idle", "", ok
	}
	s.mu.Lock()
	st, state, mux := s.stream, s.state, MuxLabel(s.mux)
	s.mu.Unlock()
	if st == nil {
		return dvb.Signal{StrengthPct: -1, SNRPct: -1, BER: -1, Updated: time.Now()}, state, mux, true
	}
	return st.Signal(), state, mux, true
}

// SimulateDrop makes a virtual tuner lose signal (for testing recovery).
func (m *Manager) SimulateDrop(key string, d time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tuners {
		if t.src.Key() == key {
			if v, ok := t.src.(*VirtualSource); ok {
				v.SimulateDrop(d)
				return nil
			}
			return errors.New("only virtual tuners can simulate drops")
		}
	}
	return errors.New("tuner not found")
}

// monitor samples signal statistics once a second.
func (m *Manager) monitor() {
	tick := time.NewTicker(time.Second)
	for range tick.C {
		m.mu.Lock()
		type job struct {
			t *Tuner
			s *session
		}
		var jobs []job
		for _, t := range m.tuners {
			if t.sess != nil {
				jobs = append(jobs, job{t, t.sess})
			}
		}
		m.mu.Unlock()
		for _, j := range jobs {
			sig, kbps, ccErr := j.s.sample()
			sm := Sample{T: time.Now().Unix(), Locked: sig.Locked, Strength: sig.StrengthPct, BER: sig.BER,
				UNC: sig.UNCDelta, CCErr: ccErr, Kbps: kbps}
			if sig.SNRdB != nil {
				sm.SNR, sm.SNRIsDB = *sig.SNRdB, true
			} else {
				sm.SNR = sig.SNRPct
			}
			m.mu.Lock()
			j.t.signal = sig
			j.t.history = append(j.t.history, sm)
			if len(j.t.history) > historyLen {
				j.t.history = j.t.history[len(j.t.history)-historyLen:]
			}
			m.mu.Unlock()
		}
	}
}

// ---- status ----

type SubStatus struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Client    string    `json:"client"`
	Weight    int       `json:"weight"`
	Service   string    `json:"service"`
	Profile   string    `json:"profile"`
	Started   time.Time `json:"started"`
	BytesOut  uint64    `json:"bytesOut"`
	Dropped   uint64    `json:"dropped"`
	State     string    `json:"state"`
	Failovers int       `json:"failovers"`
	Tuner     string    `json:"tuner"`
	Transcode any       `json:"transcode,omitempty"`
}

type TunerStatus struct {
	Key       string            `json:"key"`
	Name      string            `json:"name"`
	Virtual   bool              `json:"virtual"`
	DelSys    []string          `json:"delsys"`
	Config    store.TunerConfig `json:"config"`
	State     string            `json:"state"` // idle, tuning, streaming, nosignal
	MuxID     string            `json:"muxId,omitempty"`
	Mux       string            `json:"mux,omitempty"`
	LastError string            `json:"lastError,omitempty"`
	Retunes   int               `json:"retunes"`
	Outage    float64           `json:"outageSeconds"`
	Signal    dvb.Signal        `json:"signal"`
	Bars      int               `json:"bars"`
	Quality   string            `json:"quality"`
	Hold      string            `json:"hold,omitempty"`  // held, blocked: ...
	Users     []dvb.Proc        `json:"users,omitempty"` // other programs holding the adapter
	Kbps      float64           `json:"kbps"`
	CCErrors  uint64            `json:"ccErrors"`
	TEIErrors uint64            `json:"teiErrors"`
	Subs      []SubStatus       `json:"subscriptions"`
	History   []Sample          `json:"history"`
}

func (m *Manager) Status(withHistory bool) []TunerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []TunerStatus
	for _, t := range m.tuners {
		ts := TunerStatus{Key: t.src.Key(), Name: t.src.Name(), Virtual: t.src.Virtual(), DelSys: t.src.DelSys(),
			Config: t.cfg, State: "idle", Signal: t.signal, Bars: t.signal.Bars(), Quality: "idle"}
		if t.sess != nil {
			ts.Quality = t.signal.Quality()
		}
		if hw, ok := t.src.(*hwSource); ok {
			ts.Hold = hw.HoldState()
			ts.Users = dvb.DeviceUsers(hw.info.Adapter)
		}
		if withHistory {
			ts.History = append([]Sample(nil), t.history...)
		}
		if s := t.sess; s != nil {
			s.mu.Lock()
			ts.State = s.state
			ts.MuxID = s.mux.ID
			ts.Mux = MuxLabel(s.mux)
			if s.lastErr != nil {
				ts.LastError = s.lastErr.Error()
			}
			ts.Retunes = s.retunes
			if !s.outageStart.IsZero() {
				ts.Outage = time.Since(s.outageStart).Seconds()
			}
			ts.Kbps = s.kbps
			ts.CCErrors = s.ccErrors
			ts.TEIErrors = s.teiErrors
			s.mu.Unlock()
			for sub := range s.subs {
				ts.Subs = append(ts.Subs, sub.status())
			}
			sort.Slice(ts.Subs, func(i, j int) bool { return ts.Subs[i].ID < ts.Subs[j].ID })
		}
		out = append(out, ts)
	}
	return out
}

func (m *Manager) Subscriptions() []SubStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []SubStatus
	for _, s := range m.subs {
		out = append(out, s.status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// KillSubscription ends a subscription by id.
func (m *Manager) KillSubscription(id int) bool {
	m.mu.Lock()
	sub := m.subs[id]
	m.mu.Unlock()
	if sub == nil {
		return false
	}
	sub.end(errors.New("stopped by admin"))
	return true
}

// Hardware returns discovered frontends.
func (m *Manager) Hardware() []dvb.FrontendInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]dvb.FrontendInfo(nil), m.hw...)
}

// TunerCount returns the number of enabled tuners (for HDHomeRun emulation).
func (m *Manager) TunerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.tuners {
		if t.cfg.Enabled {
			n++
		}
	}
	return n
}

// BusyMuxes reports which muxes currently have an active session.
func (m *Manager) BusyMuxes() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for id := range m.sessions {
		out[id] = true
	}
	return out
}

// FreeTuners counts idle, enabled tuners compatible with a network.
func (m *Manager) FreeTuners(netID string) int {
	var net store.Network
	m.st.View(func(st *store.State) {
		if n, ok := st.Networks[netID]; ok {
			net = *n
		}
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.tuners {
		if (t.sess == nil || len(t.sess.subs) == 0) && t.cfg.Enabled && t.src.Virtual() == (net.Type == "virtual") &&
			(len(t.cfg.Networks) == 0 || slices.Contains(t.cfg.Networks, netID)) {
			n++
		}
	}
	return n
}
