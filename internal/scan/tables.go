package scan

import (
	"errors"
	"fmt"
	"log"
	"time"

	"dvbhub/internal/dvb"
	ts "dvbhub/internal/mpegts"
	"dvbhub/internal/tuners"
)

var errNoSignal = errors.New("no signal on this channel")

// result holds the tables read from one mux.
type result struct {
	pat  *ts.PAT
	pmts map[uint16]*ts.PMT
	sdt  *ts.SDT
	nit  *ts.NIT
	vct  []ts.VCTChannel
}

// parts collects the sections of a multi-section table.
type parts[T any] struct {
	got  map[byte]T
	last byte
}

func (p *parts[T]) add(num, last byte, v T) {
	if p.got == nil {
		p.got = map[byte]T{}
	}
	p.got[num] = v
	p.last = last
}

func (p *parts[T]) complete() bool { return len(p.got) > 0 && len(p.got) > int(p.last) }

const (
	tableWait  = 20 * time.Second // after the first packet, how long tables may take
	nitWait    = 12 * time.Second // DVB NITs repeat every 10 s at most
	siWait     = 8 * time.Second  // give up waiting for an SDT/VCT (e.g. clear QAM without PSIP)
	firstExtra = 3 * time.Second  // on top of the tuner's lock timeout
)

// scanMux reads PAT/PMT/SDT/NIT/VCT from a raw mux subscription.
func (s *Scanner) scanMux(muxID string, sub *tuners.Subscription) (*result, error) {
	s.setMuxStatus(muxID, "scanning")
	res := &result{pmts: map[uint16]*ts.PMT{}}
	asm := map[uint16]*ts.SectionAssembler{
		ts.PIDPAT: ts.NewSectionAssembler(), ts.PIDSDT: ts.NewSectionAssembler(),
		ts.PIDNIT: ts.NewSectionAssembler(), ts.PIDPSIP: ts.NewSectionAssembler(),
	}
	pmtPID := map[uint16]bool{}
	var sdt parts[*ts.SDT]
	var nit parts[*ts.NIT]
	var vct parts[*ts.VCT]
	start := time.Now()
	var firstData time.Time
	deadline := time.NewTimer(sub.TuneTimeout() + firstExtra)
	defer deadline.Stop()
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()

	complete := func() bool {
		if res.pat == nil {
			return false
		}
		for prog := range res.pat.Programs {
			if prog != 0 && res.pmts[prog] == nil {
				return false
			}
		}
		since := time.Since(firstData)
		// DVB muxes carry an SDT (and a NIT); ATSC muxes a VCT instead.
		if vct.complete() {
			return true
		}
		if !sdt.complete() && since < siWait {
			return false
		}
		return nit.complete() || since > nitWait || (!sdt.complete() && since >= siWait)
	}
	merged := func() *result {
		for _, p := range vct.got {
			res.vct = append(res.vct, p.Channels...)
		}
		for _, p := range sdt.got {
			if res.sdt == nil {
				c := *p
				res.sdt = &c
			} else {
				res.sdt.Services = append(res.sdt.Services, p.Services...)
			}
		}
		for _, p := range nit.got {
			if res.nit == nil {
				c := *p
				res.nit = &c
			} else {
				res.nit.Transports = append(res.nit.Transports, p.Transports...)
			}
		}
		return res
	}
	onSection := func(pid uint16, sec []byte) {
		ps, err := ts.ParseSection(sec)
		if err != nil || !ps.Current {
			return
		}
		switch {
		case pid == ts.PIDPAT && ps.TableID == 0x00:
			pat, err := ts.ParsePAT(ps)
			if err != nil || res.pat != nil {
				return
			}
			res.pat = pat
			for prog, pp := range pat.Programs {
				if prog == 0 {
					if pp != ts.PIDNIT && asm[pp] == nil {
						asm[pp] = ts.NewSectionAssembler()
					}
					continue
				}
				pmtPID[pp] = true
				if asm[pp] == nil {
					asm[pp] = ts.NewSectionAssembler()
				}
			}
		case ps.TableID == 0x02 && pmtPID[pid]:
			if pmt, err := ts.ParsePMT(ps); err == nil {
				res.pmts[pmt.Program] = pmt
			}
		case ps.TableID == 0x42:
			if t, err := ts.ParseSDT(ps); err == nil {
				sdt.add(ps.Number, ps.Last, t)
			}
		case ps.TableID == 0x40:
			if t, err := ts.ParseNIT(ps); err == nil {
				nit.add(ps.Number, ps.Last, t)
			}
		case pid == ts.PIDPSIP && ts.IsVCT(ps.TableID):
			if t, err := ts.ParseVCT(ps); err == nil {
				vct.add(ps.Number, ps.Last, t)
			}
		}
	}

	for {
		select {
		case <-sub.Done:
			if err := sub.Err(); err != nil {
				return nil, fmt.Errorf("stopped: %v", err)
			}
			return nil, errors.New("stopped")
		case <-poll.C:
			if firstData.IsZero() {
				if err := sub.TuneFailure(); errors.Is(err, dvb.ErrNoSignal) {
					return nil, errNoSignal
				}
			}
		case <-deadline.C:
			if firstData.IsZero() {
				if err := sub.TuneFailure(); err != nil {
					if errors.Is(err, dvb.ErrNoSignal) {
						return nil, errNoSignal
					}
					return nil, err
				}
				return nil, errors.New("no data from the tuner (no lock)")
			}
			if res.pat == nil {
				return nil, errors.New("locked, but no programme table (PAT) arrived")
			}
			return merged(), nil
		case pkts := <-sub.C:
			if firstData.IsZero() {
				firstData = time.Now()
				deadline.Reset(tableWait)
			}
			ts.ForEach(pkts, func(p []byte) {
				pid := ts.PID(p)
				if a := asm[pid]; a != nil {
					a.Push(p, func(sec []byte) { onSection(pid, sec) })
				}
			})
			if complete() {
				log.Printf("scan: tables of %s read in %s", s.muxName(muxID), time.Since(start).Round(time.Millisecond))
				return merged(), nil
			}
		}
	}
}
