//go:build linux

// Package dvb drives Linux DVB API v5 frontends and demuxers directly via
// ioctl, without libdvbv5.
package dvb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"dvbhub/internal/store"
)

// ioctl request numbers, verified against linux/dvb/*.h (DVB API 5.12, amd64).
const (
	feGetInfo          = 0x80a86f3d
	feReadStatus       = 0x80046f45
	feReadBER          = 0x80046f46
	feReadSignal       = 0x80026f47
	feReadSNR          = 0x80026f48
	feReadUNC          = 0x80046f49
	feSetProperty      = 0x40106f52
	feGetProperty      = 0x80106f53
	feSetTone          = 0x6f42
	feSetVoltage       = 0x6f43
	feDiseqcSendMaster = 0x40076f3f
	feDiseqcSendBurst  = 0x6f41
	dmxStop            = 0x6f2a
	dmxSetPESFilter    = 0x40146f2c
	dmxSetBufferSize   = 0x6f2d

	dtvTune             = 1
	dtvClear            = 2
	dtvFrequency        = 3
	dtvModulation       = 4
	dtvBandwidthHz      = 5
	dtvInversion        = 6
	dtvSymbolRate       = 8
	dtvInnerFEC         = 9
	dtvVoltage          = 10
	dtvTone             = 11
	dtvPilot            = 12
	dtvRolloff          = 13
	dtvDeliverySystem   = 17
	dtvCodeRateHP       = 36
	dtvCodeRateLP       = 37
	dtvGuardInterval    = 38
	dtvTransmissionMode = 39
	dtvHierarchy        = 40
	dtvStreamID         = 42
	dtvEnumDelsys       = 44
	dtvStatSignal       = 62
	dtvStatCNR          = 63
	dtvStatPreErrBits   = 64
	dtvStatPreTotalBits = 65
	dtvStatErrBlocks    = 68
	dtvStatTotalBlocks  = 69

	propSize   = 76 // sizeof(struct dtv_property), packed
	propUnion  = 16
	propResult = 72

	feHasSignal  = 0x01
	feHasCarrier = 0x02
	feHasViterbi = 0x04
	feHasSync    = 0x08
	feHasLock    = 0x10
)

var delsysByName = map[string]uint32{
	"DVB-C": 1, "DVB-C/B": 2, "DVB-T": 3, "DVB-S": 5, "DVB-S2": 6, "ISDB-T": 8, "ATSC": 11, "DTMB": 13, "DVB-T2": 16, "DVB-C/C": 18,
}

func delsysName(v uint32) string {
	for k, n := range delsysByName {
		if n == v {
			return k
		}
	}
	return fmt.Sprintf("SYS_%d", v)
}

var (
	modulations = map[string]uint32{"QPSK": 0, "QAM/16": 1, "QAM/32": 2, "QAM/64": 3, "QAM/128": 4, "QAM/256": 5, "AUTO": 6, "8VSB": 7, "16VSB": 8, "8PSK": 9, "16APSK": 10, "32APSK": 11, "DQPSK": 12}
	fecs        = map[string]uint32{"NONE": 0, "1/2": 1, "2/3": 2, "3/4": 3, "4/5": 4, "5/6": 5, "6/7": 6, "7/8": 7, "8/9": 8, "AUTO": 9, "3/5": 10, "9/10": 11, "2/5": 12}
	txModes     = map[string]uint32{"2K": 0, "8K": 1, "AUTO": 2, "4K": 3, "1K": 4, "16K": 5, "32K": 6}
	guards      = map[string]uint32{"1/32": 0, "1/16": 1, "1/8": 2, "1/4": 3, "AUTO": 4, "1/128": 5, "19/128": 6, "19/256": 7}
	hierarchies = map[string]uint32{"NONE": 0, "1": 1, "2": 2, "4": 3, "AUTO": 4}
	pilots      = map[string]uint32{"ON": 0, "OFF": 1, "AUTO": 2}
	rolloffs    = map[string]uint32{"35": 0, "20": 1, "25": 2, "AUTO": 3}
	inversions  = map[string]uint32{"OFF": 0, "ON": 1, "AUTO": 2}
)

func lookup(m map[string]uint32, key string, def uint32) uint32 {
	if v, ok := m[strings.ToUpper(strings.TrimSpace(key))]; ok {
		return v
	}
	return def
}

func ioctl(f *os.File, req uintptr, arg uintptr) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	cerr := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
	})
	if cerr != nil {
		return cerr
	}
	if errno != 0 {
		return errno
	}
	return nil
}

type dtvProperties struct {
	num   uint32
	props uintptr
}

type propList struct {
	buf []byte
	n   int
}

func newPropList(max int) *propList { return &propList{buf: make([]byte, propSize*max)} }

func (p *propList) add(cmd, data uint32) {
	b := p.buf[p.n*propSize:]
	binary.NativeEndian.PutUint32(b, cmd)
	binary.NativeEndian.PutUint32(b[propUnion:], data)
	p.n++
}

func (p *propList) prop(i int) []byte { return p.buf[i*propSize : (i+1)*propSize] }

func (p *propList) call(f *os.File, req uintptr) error {
	arg := &dtvProperties{num: uint32(p.n), props: uintptr(unsafe.Pointer(&p.buf[0]))}
	err := ioctl(f, req, uintptr(unsafe.Pointer(arg)))
	runtime.KeepAlive(arg)
	runtime.KeepAlive(p.buf)
	return err
}

// FrontendInfo describes a DVB frontend found on the system.
type FrontendInfo struct {
	Key      string   `json:"key"`
	Adapter  int      `json:"adapter"`
	Frontend int      `json:"frontend"`
	Name     string   `json:"name"`
	DelSys   []string `json:"delsys"`
}

// Discover lists all DVB frontends under /dev/dvb.
func Discover() []FrontendInfo {
	paths, _ := filepath.Glob("/dev/dvb/adapter*/frontend*")
	sort.Strings(paths)
	var out []FrontendInfo
	for _, p := range paths {
		var a, fe int
		if _, err := fmt.Sscanf(p, "/dev/dvb/adapter%d/frontend%d", &a, &fe); err != nil {
			continue
		}
		info := FrontendInfo{Key: fmt.Sprintf("adapter%d/frontend%d", a, fe), Adapter: a, Frontend: fe}
		f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			info.Name = "unavailable: " + err.Error()
			out = append(out, info)
			continue
		}
		var raw [168]byte
		if ioctl(f, feGetInfo, uintptr(unsafe.Pointer(&raw[0]))) == nil {
			info.Name = strings.TrimRight(string(raw[:128]), "\x00")
		}
		pl := newPropList(1)
		pl.add(dtvEnumDelsys, 0)
		if pl.call(f, feGetProperty) == nil {
			b := pl.prop(0)
			n := int(binary.NativeEndian.Uint32(b[propUnion+32:]))
			for i := 0; i < n && i < 32; i++ {
				info.DelSys = append(info.DelSys, delsysName(uint32(b[propUnion+i])))
			}
		}
		f.Close()
		out = append(out, info)
	}
	return out
}

// Signal is a snapshot of frontend reception quality.
type Signal struct {
	Locked      bool      `json:"locked"`
	Status      []string  `json:"status"`
	StrengthPct float64   `json:"strengthPct"` // -1 if unknown
	StrengthDBm *float64  `json:"strengthDbm,omitempty"`
	SNRdB       *float64  `json:"snrDb,omitempty"`
	SNRPct      float64   `json:"snrPct"` // -1 if unknown
	BER         float64   `json:"ber"`    // bit error ratio over the last interval, -1 if unknown
	UNC         uint64    `json:"unc"`    // uncorrected blocks since tune
	UNCDelta    uint64    `json:"uncDelta"`
	Source      string    `json:"source"` // dvbv5, legacy
	Updated     time.Time `json:"updated"`
}

// Frontend is an opened DVB frontend.
type Frontend struct {
	Adapter, Index int
	f              *os.File

	prevErrBits, prevTotalBits, prevUNC uint64
	uncBase                             uint64
	uncBaseSet                          bool
}

func OpenFrontend(adapter, index int) (*Frontend, error) {
	f, err := os.OpenFile(fmt.Sprintf("/dev/dvb/adapter%d/frontend%d", adapter, index), os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return &Frontend{Adapter: adapter, Index: index, f: f}, nil
}

func (fe *Frontend) Close() error { return fe.f.Close() }

// Idle turns LNB power off while the device is held open but not in use.
// It is a no-op for frontends without LNB control.
func (fe *Frontend) Idle() {
	_ = ioctl(fe.f, feSetVoltage, 2) // SEC_VOLTAGE_OFF
}

// Tune programs the frontend and waits for lock.
func (fe *Frontend) Tune(t store.Tuning, sat *store.SatInput, timeout time.Duration) error {
	ds, ok := delsysByName[strings.ToUpper(t.DeliverySystem)]
	if !ok {
		return fmt.Errorf("unsupported delivery system %q", t.DeliverySystem)
	}
	fe.uncBaseSet = false
	pl := newPropList(20)
	clr := newPropList(1)
	clr.add(dtvClear, 0)
	if err := clr.call(fe.f, feSetProperty); err != nil {
		return fmt.Errorf("DTV_CLEAR: %w", err)
	}
	pl.add(dtvDeliverySystem, ds)
	switch t.DeliverySystem {
	case "DVB-S", "DVB-S2":
		ifreq, voltage, tone := satIF(t, sat)
		if err := fe.diseqc(sat, t, voltage, tone); err != nil {
			return err
		}
		pl.add(dtvFrequency, ifreq)
		pl.add(dtvVoltage, voltage)
		pl.add(dtvTone, tone)
		pl.add(dtvSymbolRate, t.SymbolRate)
		pl.add(dtvInnerFEC, lookup(fecs, t.FEC, 9))
		pl.add(dtvInversion, lookup(inversions, t.Inversion, 2))
		if t.DeliverySystem == "DVB-S2" {
			pl.add(dtvModulation, lookup(modulations, t.Modulation, 9))
			pl.add(dtvPilot, lookup(pilots, t.Pilot, 2))
			pl.add(dtvRolloff, lookup(rolloffs, t.Rolloff, 3))
			if t.StreamID >= 0 {
				pl.add(dtvStreamID, uint32(t.StreamID))
			}
		} else {
			pl.add(dtvModulation, 0)
		}
	case "DVB-C", "DVB-C/B", "DVB-C/C":
		pl.add(dtvFrequency, t.FrequencyKHz*1000)
		pl.add(dtvSymbolRate, t.SymbolRate)
		pl.add(dtvModulation, lookup(modulations, t.Modulation, 6))
		pl.add(dtvInnerFEC, lookup(fecs, t.FEC, 9))
		pl.add(dtvInversion, lookup(inversions, t.Inversion, 2))
	case "ATSC":
		pl.add(dtvFrequency, t.FrequencyKHz*1000)
		pl.add(dtvModulation, lookup(modulations, t.Modulation, 7))
		pl.add(dtvInversion, lookup(inversions, t.Inversion, 2))
	default: // DVB-T, DVB-T2, ISDB-T, DTMB
		pl.add(dtvFrequency, t.FrequencyKHz*1000)
		bw := t.BandwidthHz
		if bw == 0 {
			bw = 8000000
		}
		pl.add(dtvBandwidthHz, bw)
		pl.add(dtvModulation, lookup(modulations, t.Modulation, 6))
		pl.add(dtvCodeRateHP, lookup(fecs, t.FEC, 9))
		pl.add(dtvCodeRateLP, lookup(fecs, t.CodeRateLP, 9))
		pl.add(dtvTransmissionMode, lookup(txModes, t.TransmissionMode, 2))
		pl.add(dtvGuardInterval, lookup(guards, t.GuardInterval, 4))
		pl.add(dtvHierarchy, lookup(hierarchies, t.Hierarchy, 4))
		pl.add(dtvInversion, lookup(inversions, t.Inversion, 2))
		if t.DeliverySystem == "DVB-T2" && t.StreamID >= 0 {
			pl.add(dtvStreamID, uint32(t.StreamID))
		}
	}
	pl.add(dtvTune, 0)
	if err := pl.call(fe.f, feSetProperty); err != nil {
		return fmt.Errorf("FE_SET_PROPERTY: %w", err)
	}
	return fe.WaitLock(timeout)
}

// WaitLock polls the frontend until it reports lock or the timeout expires.
func (fe *Frontend) WaitLock(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := fe.status()
		if err == nil && st&feHasLock != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("no lock: %w", err)
			}
			return fmt.Errorf("no lock (status 0x%02x: %s)", st, strings.Join(statusNames(st), ","))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (fe *Frontend) status() (uint32, error) {
	var st uint32
	err := ioctl(fe.f, feReadStatus, uintptr(unsafe.Pointer(&st)))
	return st, err
}

func statusNames(st uint32) []string {
	names := []string{}
	for _, x := range []struct {
		bit  uint32
		name string
	}{{feHasSignal, "signal"}, {feHasCarrier, "carrier"}, {feHasViterbi, "viterbi"}, {feHasSync, "sync"}, {feHasLock, "lock"}} {
		if st&x.bit != 0 {
			names = append(names, x.name)
		}
	}
	return names
}

// satIF computes the intermediate frequency, LNB voltage and 22 kHz tone.
func satIF(t store.Tuning, sat *store.SatInput) (ifreq, voltage, tone uint32) {
	s := store.SatInput{LNB: "universal", LOFLow: 9750000, LOFHigh: 10600000, Switch: 11700000}
	if sat != nil && sat.LNB != "" {
		s = *sat
	}
	voltage = 0 // 13 V: vertical / right
	if p := strings.ToUpper(t.Polarization); p == "H" || p == "L" {
		voltage = 1 // 18 V
	}
	tone = 1 // off
	lof := s.LOFLow
	switch s.LNB {
	case "none":
		return t.FrequencyKHz, voltage, tone
	case "universal":
		if s.Switch != 0 && t.FrequencyKHz >= s.Switch {
			lof = s.LOFHigh
			tone = 0
		}
	}
	if lof == 0 {
		lof = 9750000
	}
	if t.FrequencyKHz > lof {
		return t.FrequencyKHz - lof, voltage, tone
	}
	return lof - t.FrequencyKHz, voltage, tone
}

func (fe *Frontend) diseqc(sat *store.SatInput, t store.Tuning, voltage, tone uint32) error {
	if sat == nil || sat.DiseqcPort <= 0 {
		return nil
	}
	if err := ioctl(fe.f, feSetTone, 1); err != nil {
		return fmt.Errorf("tone off: %w", err)
	}
	if err := ioctl(fe.f, feSetVoltage, uintptr(voltage)); err != nil {
		return fmt.Errorf("voltage: %w", err)
	}
	time.Sleep(15 * time.Millisecond)
	b := byte(0xf0) | byte((sat.DiseqcPort-1)&3)<<2
	if voltage == 1 {
		b |= 2
	}
	if tone == 0 {
		b |= 1
	}
	cmd := [7]byte{0xe0, 0x10, 0x38, b, 0, 0, 4}
	if err := ioctl(fe.f, feDiseqcSendMaster, uintptr(unsafe.Pointer(&cmd[0]))); err != nil {
		return fmt.Errorf("diseqc: %w", err)
	}
	time.Sleep(15 * time.Millisecond)
	burst := uintptr((sat.DiseqcPort - 1) & 1)
	_ = ioctl(fe.f, feDiseqcSendBurst, burst)
	time.Sleep(15 * time.Millisecond)
	return nil
}

// Signal reads reception statistics, preferring DVBv5 stats with legacy fallback.
func (fe *Frontend) Signal() Signal {
	s := Signal{StrengthPct: -1, SNRPct: -1, BER: -1, Updated: time.Now()}
	st, err := fe.status()
	if err == nil {
		s.Locked = st&feHasLock != 0
		s.Status = statusNames(st)
	}
	pl := newPropList(6)
	for _, c := range []uint32{dtvStatSignal, dtvStatCNR, dtvStatPreErrBits, dtvStatPreTotalBits, dtvStatErrBlocks, dtvStatTotalBlocks} {
		pl.add(c, 0)
	}
	haveV5 := false
	if pl.call(fe.f, feGetProperty) == nil {
		stat := func(i int) (scale byte, val int64, ok bool) {
			b := pl.prop(i)
			if b[propUnion] == 0 { // len
				return 0, 0, false
			}
			scale = b[propUnion+1]
			val = int64(binary.NativeEndian.Uint64(b[propUnion+2:]))
			return scale, val, scale != 0
		}
		if sc, v, ok := stat(0); ok {
			haveV5 = true
			if sc == 1 {
				dbm := float64(v) / 1000
				s.StrengthDBm = &dbm
				// Map roughly -100 dBm .. -20 dBm onto 0..100 % for display.
				s.StrengthPct = clamp((dbm + 100) * 100 / 80)
			} else if sc == 2 {
				s.StrengthPct = float64(uint16(v)) * 100 / 65535
			}
		}
		if sc, v, ok := stat(1); ok {
			haveV5 = true
			if sc == 1 {
				db := float64(v) / 1000
				s.SNRdB = &db
				s.SNRPct = clamp(db * 100 / 30)
			} else if sc == 2 {
				s.SNRPct = float64(uint16(v)) * 100 / 65535
			}
		}
		_, eb, ok1 := stat(2)
		_, tb, ok2 := stat(3)
		if ok1 && ok2 {
			de, dt := uint64(eb)-fe.prevErrBits, uint64(tb)-fe.prevTotalBits
			if dt > 0 && uint64(tb) >= fe.prevTotalBits {
				s.BER = float64(de) / float64(dt)
			}
			fe.prevErrBits, fe.prevTotalBits = uint64(eb), uint64(tb)
		}
		if _, ub, ok := stat(4); ok {
			fe.setUNC(&s, uint64(ub))
		}
	}
	if !haveV5 {
		s.Source = "legacy"
		var u16 uint16
		if ioctl(fe.f, feReadSignal, uintptr(unsafe.Pointer(&u16))) == nil {
			s.StrengthPct = float64(u16) * 100 / 65535
		}
		if ioctl(fe.f, feReadSNR, uintptr(unsafe.Pointer(&u16))) == nil {
			s.SNRPct = float64(u16) * 100 / 65535
		}
		var u32 uint32
		if ioctl(fe.f, feReadBER, uintptr(unsafe.Pointer(&u32))) == nil {
			s.BER = float64(u32) / 1e7 // driver specific; shown as relative indicator
		}
		if ioctl(fe.f, feReadUNC, uintptr(unsafe.Pointer(&u32))) == nil {
			fe.setUNC(&s, uint64(u32))
		}
	} else {
		s.Source = "dvbv5"
	}
	return s
}

func (fe *Frontend) setUNC(s *Signal, v uint64) {
	if !fe.uncBaseSet || v < fe.uncBase {
		fe.uncBase, fe.prevUNC, fe.uncBaseSet = v, v, true
	}
	s.UNC = v - fe.uncBase
	if v >= fe.prevUNC {
		s.UNCDelta = v - fe.prevUNC
	}
	fe.prevUNC = v
}

func clamp(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	}
	return v
}

// Demux is a full-transport-stream tap on a DVB demuxer.
type Demux struct {
	f *os.File
}

// OpenDemux opens the adapter's demuxer and taps the whole transport stream.
func OpenDemux(adapter, index int) (*Demux, error) {
	path := fmt.Sprintf("/dev/dvb/adapter%d/demux%d", adapter, index)
	if _, err := os.Stat(path); err != nil {
		path = fmt.Sprintf("/dev/dvb/adapter%d/demux0", adapter)
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	_ = ioctl(f, dmxSetBufferSize, 8<<20)
	params := struct {
		PID     uint16
		_       uint16
		Input   uint32
		Output  uint32
		PESType uint32
		Flags   uint32
	}{PID: 0x2000, Input: 0, Output: 3 /* TSDEMUX_TAP */, PESType: 20 /* OTHER */, Flags: 4 /* IMMEDIATE_START */}
	if err := ioctl(f, dmxSetPESFilter, uintptr(unsafe.Pointer(&params))); err != nil {
		f.Close()
		return nil, fmt.Errorf("DMX_SET_PES_FILTER: %w", err)
	}
	return &Demux{f: f}, nil
}

// Read reads transport stream data, skipping buffer overflow notifications.
func (d *Demux) Read(p []byte) (int, error) {
	for {
		n, err := d.f.Read(p)
		if err != nil && errors.Is(err, syscall.EOVERFLOW) {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.ErrNoProgress
		}
		return n, err
	}
}

// SetDeadline bounds the next Read so stalls can be detected.
func (d *Demux) SetDeadline(t time.Time) error { return d.f.SetReadDeadline(t) }

func (d *Demux) Close() error {
	_ = ioctl(d.f, dmxStop, 0)
	return d.f.Close()
}
