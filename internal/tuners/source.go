package tuners

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"sync"
	"syscall"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/dvb"
	ts "dvbhub/internal/mpegts"
)

// Source is something that can be tuned to a mux and yield a transport stream.
type Source interface {
	Key() string
	Name() string
	DelSys() []string
	Virtual() bool
	Open(ctx context.Context, mux config.Mux, sat *dvb.SatInput, timeout time.Duration) (Stream, error)
}

// Stream is an open, tuned transport stream.
type Stream interface {
	Read(p []byte) (int, error)
	SetDeadline(t time.Time) error
	Signal() dvb.Signal
	Close() error
}

// ---- hardware ----

type hwSource struct {
	info dvb.FrontendInfo

	mu      sync.Mutex
	held    *dvb.Frontend // open while "hold" is on, so no other program can tune it
	holdErr error
}

func (h *hwSource) Key() string { return h.info.Key } // never changes

func (h *hwSource) Name() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info.Name
}

func (h *hwSource) DelSys() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info.DelSys
}

func (h *hwSource) Virtual() bool { return false }

// Responding reports whether the driver answered the last discovery probe.
func (h *hwSource) Responding() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info.OK
}

func (h *hwSource) setInfo(info dvb.FrontendInfo) {
	h.mu.Lock()
	h.info = info
	h.mu.Unlock()
}

// SetHold opens (and keeps open) or releases the frontend. Linux allows only
// one read-write opener per frontend, so holding it locks other programs out.
func (h *hwSource) SetHold(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case on && h.held == nil:
		fe, err := dvb.OpenFrontend(h.info.Adapter, h.info.Frontend)
		if err != nil {
			h.holdErr = busyErr(err, h.info.Adapter)
			return
		}
		fe.Idle()
		h.held, h.holdErr = fe, nil
	case !on && h.held != nil:
		h.held.Close()
		h.held = nil
		h.holdErr = nil
	case !on:
		h.holdErr = nil
	}
}

// HoldState reports "held", "blocked: ..." or "" when not holding.
func (h *hwSource) HoldState() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.held != nil:
		return "held"
	case h.holdErr != nil:
		return "blocked: " + h.holdErr.Error()
	}
	return ""
}

func busyErr(err error, adapter int) error {
	if errors.Is(err, syscall.EBUSY) {
		return fmt.Errorf("device busy, in use by %s", dvb.DescribeUsers(adapter))
	}
	return err
}

func (h *hwSource) Open(ctx context.Context, mux config.Mux, sat *dvb.SatInput, timeout time.Duration) (Stream, error) {
	h.mu.Lock()
	fe, held := h.held, h.held != nil
	h.mu.Unlock()
	if !held {
		var err error
		fe, err = dvb.OpenFrontend(h.info.Adapter, h.info.Frontend)
		if err != nil {
			return nil, busyErr(err, h.info.Adapter)
		}
	}
	release := func() {
		if held {
			fe.Idle()
		} else {
			fe.Close()
		}
	}
	if err := fe.Tune(ctx, mux.Tuning, sat, timeout); err != nil {
		release()
		return nil, err
	}
	dmx, err := dvb.OpenDemux(h.info.Adapter, h.info.Frontend)
	if err != nil {
		release()
		return nil, busyErr(err, h.info.Adapter)
	}
	return &hwStream{fe: fe, dmx: dmx, release: release}, nil
}

type hwStream struct {
	fe      *dvb.Frontend
	dmx     *dvb.Demux
	release func()
	mu      sync.Mutex
}

func (s *hwStream) Read(p []byte) (int, error)    { return s.dmx.Read(p) }
func (s *hwStream) SetDeadline(t time.Time) error { return s.dmx.SetDeadline(t) }
func (s *hwStream) Signal() dvb.Signal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fe.Signal()
}
func (s *hwStream) Close() error {
	s.dmx.Close()
	s.release()
	return nil
}

// ---- virtual (file backed) ----

// VirtualSource plays TS files at their real-time rate. It is used for
// testing without hardware and supports simulated signal drops.
type VirtualSource struct {
	key string

	mu        sync.Mutex
	dropUntil time.Time
}

func NewVirtualSource(i int) *VirtualSource { return &VirtualSource{key: fmt.Sprintf("virtual%d", i)} }

func (v *VirtualSource) Key() string      { return v.key }
func (v *VirtualSource) Name() string     { return "Virtual file tuner" }
func (v *VirtualSource) DelSys() []string { return []string{"VIRTUAL"} }
func (v *VirtualSource) Virtual() bool    { return true }

// SimulateDrop makes the tuner lose signal for d.
func (v *VirtualSource) SimulateDrop(d time.Duration) {
	v.mu.Lock()
	v.dropUntil = time.Now().Add(d)
	v.mu.Unlock()
}

func (v *VirtualSource) dropped() (bool, time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return time.Now().Before(v.dropUntil), v.dropUntil
}

func (v *VirtualSource) Open(ctx context.Context, mux config.Mux, sat *dvb.SatInput, timeout time.Duration) (Stream, error) {
	// Like real hardware, lock as soon as the signal is back, else time out.
	if d, until := v.dropped(); d {
		wait := min(time.Until(until)+100*time.Millisecond, timeout)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if d, _ := v.dropped(); d {
			return nil, errors.New("no lock (simulated)")
		}
	}
	if mux.File == "" {
		return nil, errors.New("virtual mux has no file")
	}
	f, err := os.Open(mux.File)
	if err != nil {
		return nil, err
	}
	return &virtualStream{src: v, f: f, r: bufio.NewReaderSize(f, 1<<16)}, nil
}

type virtualStream struct {
	src      *VirtualSource
	f        *os.File
	r        *bufio.Reader
	deadline time.Time

	pcrPID   int
	pcr0     uint64
	lastPCR  uint64
	wall0    time.Time
	haveBase bool
}

func (s *virtualStream) SetDeadline(t time.Time) error { s.deadline = t; return nil }

func (s *virtualStream) Signal() dvb.Signal {
	sig := dvb.Signal{Source: "virtual", Updated: time.Now(), BER: 0}
	if d, _ := s.src.dropped(); d {
		sig.Status = []string{}
		sig.StrengthPct, sig.SNRPct, sig.BER = 3, 0, -1
		return sig
	}
	snr := 24 + rand.Float64()*2
	dbm := -52 - rand.Float64()*3
	sig.Locked = true
	sig.Status = []string{"signal", "carrier", "viterbi", "sync", "lock"}
	sig.StrengthDBm = &dbm
	sig.StrengthPct = (dbm + 100) * 100 / 80
	sig.SNRdB = &snr
	sig.SNRPct = snr * 100 / 30
	return sig
}

func (s *virtualStream) Read(p []byte) (int, error) {
	if d, until := s.src.dropped(); d {
		wait := time.Until(until)
		if !s.deadline.IsZero() && time.Until(s.deadline) < wait {
			wait = time.Until(s.deadline)
		}
		time.Sleep(wait)
		s.haveBase = false
		if d, _ := s.src.dropped(); d {
			return 0, os.ErrDeadlineExceeded
		}
	}
	max := len(p) / ts.PacketSize
	if max > 64 {
		max = 64
	}
	n := 0
	var pcr uint64
	havePCR := false
	for i := 0; i < max; i++ {
		pkt := p[n : n+ts.PacketSize]
		if _, err := io.ReadFull(s.r, pkt); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				s.f.Seek(0, io.SeekStart)
				s.r.Reset(s.f)
				s.haveBase = false
				break
			}
			return n, err
		}
		if pkt[0] != ts.SyncByte {
			continue
		}
		if v, ok := ts.PCR(pkt); ok {
			pid := int(ts.PID(pkt))
			if s.pcrPID == 0 {
				s.pcrPID = pid
			}
			if pid == s.pcrPID {
				pcr, havePCR = v, true
			}
		}
		n += ts.PacketSize
	}
	if havePCR {
		s.pace(pcr)
	} else if n == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	return n, nil
}

func (s *virtualStream) pace(pcr uint64) {
	now := time.Now()
	if !s.haveBase || pcr < s.lastPCR || pcr-s.lastPCR > 27_000_000 {
		s.pcr0, s.wall0, s.haveBase = pcr, now, true
	}
	s.lastPCR = pcr
	due := s.wall0.Add(time.Duration((pcr - s.pcr0) * 1000 / 27))
	if d := due.Sub(now); d > 0 {
		time.Sleep(d)
	}
}

func (s *virtualStream) Close() error { return s.f.Close() }
