// Command tsinject adds a NIT (with logical channel numbers) and EIT
// present/following + schedule tables to a transport stream, for testing.
//
//	tsinject -in a.ts -out b.ts -nid 12345 -lcn 101=1,102=2 -tsid 1 -onid 9018
//
// With -atsc it instead strips the SDT and injects an ATSC TVCT:
//
//	tsinject -in a.ts -out b.ts -atsc -tsid 3 -vct "301=WTST-HD:3.1,302=WTST-SD:3.2"
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	ts "dvbhub/internal/mpegts"
)

func main() {
	in := flag.String("in", "", "input TS")
	out := flag.String("out", "", "output TS")
	nid := flag.Int("nid", 12345, "network id")
	tsid := flag.Int("tsid", 1, "transport stream id")
	onid := flag.Int("onid", 9018, "original network id")
	freq := flag.Int("freq", 506000, "advertised frequency in kHz")
	lcns := flag.String("lcn", "", "sid=lcn,... ")
	titles := flag.String("titles", "", "sid=Title Prefix,...")
	every := flag.Int("every", 1500, "insert tables every N packets")
	atsc := flag.Bool("atsc", false, "ATSC mode: drop SDT/NIT, inject a TVCT from -vct")
	vcts := flag.String("vct", "", "sid=NAME:major.minor,... (ATSC mode)")
	flag.Parse()

	data, err := os.ReadFile(*in)
	if err != nil {
		log.Fatal(err)
	}
	lcn := map[uint16]int{}
	var sids []uint16
	for _, kv := range strings.Split(*lcns, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		sid, _ := strconv.Atoi(k)
		n, _ := strconv.Atoi(v)
		lcn[uint16(sid)] = n
		sids = append(sids, uint16(sid))
	}
	names := map[uint16]string{}
	for _, kv := range strings.Split(*titles, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			sid, _ := strconv.Atoi(k)
			names[uint16(sid)] = v
		}
	}

	nit := ts.BuildNIT(uint16(*nid), 0, "Test Network", []ts.NITTransport{{TSID: uint16(*tsid), ONID: uint16(*onid),
		Delivery: &ts.Delivery{FrequencyKHz: uint32(*freq)}, LCN: lcn}})
	var eits [][]byte
	base := time.Now().UTC().Truncate(30 * time.Minute).Add(-time.Hour)
	for _, sid := range sids {
		prefix := names[sid]
		if prefix == "" {
			prefix = fmt.Sprintf("Programme %d", sid)
		}
		var evs []ts.Event
		for i := 0; i < 24; i++ {
			evs = append(evs, ts.Event{EventID: uint16(i + 1), Start: base.Add(time.Duration(i) * 30 * time.Minute), Duration: 30 * time.Minute,
				Running: 1, Title: fmt.Sprintf("%s %d", prefix, i+1), Subtitle: fmt.Sprintf("Episode %d", i+1),
				Description: fmt.Sprintf("Test description for %s episode %d.", prefix, i+1), Genres: []byte{0x20 + byte(i%3)*0x10}})
		}
		// schedule in chunks of 8 events per section
		for sec := 0; sec*8 < len(evs); sec++ {
			end := min((sec+1)*8, len(evs))
			eits = append(eits, ts.BuildEIT(0x50, sid, uint16(*tsid), uint16(*onid), 1, byte(sec*8), byte((len(evs)-1)/8*8), evs[sec*8:end]))
		}
		eits = append(eits, ts.BuildEIT(0x4e, sid, uint16(*tsid), uint16(*onid), 1, 0, 0, evs[2:3]))
	}

	var vct []byte
	type psipTable struct {
		pid uint16
		sec []byte
	}
	var atscTables []psipTable
	if *atsc {
		var chans []ts.VCTChannel
		for _, kv := range strings.Split(*vcts, ",") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			name, num, _ := strings.Cut(v, ":")
			maj, mnr, _ := strings.Cut(num, ".")
			sid, _ := strconv.Atoi(k)
			a, _ := strconv.Atoi(maj)
			b, _ := strconv.Atoi(mnr)
			chans = append(chans, ts.VCTChannel{ShortName: name, Major: a, Minor: b, TSID: uint16(*tsid), Program: uint16(sid),
				ServiceType: 0x02, SourceID: uint16(1000 + sid)})
		}
		vct = ts.BuildVCT(uint16(*tsid), 0, chans)
		// PSIP guide: MGT pointing at EIT-0 and ETT-0, an STT, and 12 h of
		// half-hour programmes per channel with descriptions.
		const eitPID, ettPID = 0x1d00, 0x1e00
		atscTables = append(atscTables, psipTable{ts.PIDPSIP, ts.BuildMGT([]ts.MGTTable{{Type: 0x0100, PID: eitPID}, {Type: 0x0200, PID: ettPID}})},
			psipTable{ts.PIDPSIP, ts.BuildSTT(time.Now(), ts.DefaultGPSUTCOffset)})
		base := time.Now().UTC().Truncate(30 * time.Minute).Add(-time.Hour)
		for _, c := range chans {
			var evs []ts.ATSCEvent
			for i := 0; i < 24; i++ {
				ev := ts.ATSCEvent{EventID: uint16(i + 1), Start: ts.GPSSeconds(base.Add(time.Duration(i)*30*time.Minute), ts.DefaultGPSUTCOffset),
					Duration: 30 * time.Minute, Title: fmt.Sprintf("%s Show %d", c.ShortName, i+1)}
				evs = append(evs, ev)
				atscTables = append(atscTables, psipTable{ettPID, ts.BuildETT(c.SourceID, ev.EventID, 0, fmt.Sprintf("About %s show %d.", c.ShortName, i+1))})
			}
			atscTables = append(atscTables, psipTable{eitPID, ts.BuildATSCEIT(c.SourceID, 0, evs)})
		}
	}

	var nitCC, eitCC byte
	ccs := map[uint16]*byte{}
	var outBuf []byte
	n := 0
	for i := 0; i+ts.PacketSize <= len(data); i += ts.PacketSize {
		if *atsc {
			if pid := ts.PID(data[i:]); pid == ts.PIDSDT || pid == ts.PIDNIT {
				continue
			}
			if n%*every == 0 {
				for _, t := range append([]psipTable{{ts.PIDPSIP, vct}}, atscTables...) {
					if ccs[t.pid] == nil {
						ccs[t.pid] = new(byte)
					}
					outBuf = append(outBuf, ts.Packetize(t.pid, t.sec, ccs[t.pid])...)
				}
			}
		} else if n%*every == 0 {
			outBuf = append(outBuf, ts.Packetize(ts.PIDNIT, nit, &nitCC)...)
			for _, e := range eits {
				outBuf = append(outBuf, ts.Packetize(ts.PIDEIT, e, &eitCC)...)
			}
		}
		outBuf = append(outBuf, data[i:i+ts.PacketSize]...)
		n++
	}
	if err := os.WriteFile(*out, outBuf, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("wrote %s: %d packets + tables for %d services", *out, len(outBuf)/ts.PacketSize, len(sids))
}
