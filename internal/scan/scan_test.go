package scan

import (
	"testing"

	"dvbhub/internal/config"
	"dvbhub/internal/dvb"
	ts "dvbhub/internal/mpegts"
)

func newStore(t *testing.T) *config.Store {
	t.Helper()
	st, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// An ATSC mux: names and numbers come from the VCT, an audio-only service
// the VCT calls TV is radio, and a hidden data service is "other".
func TestStoreServicesATSC(t *testing.T) {
	st := newStore(t)
	st.Update(func(s *config.State) error {
		s.Networks["n"] = &config.Network{ID: "n", Type: "atsc"}
		s.Muxes["m"] = &config.Mux{ID: "m", NetworkID: "n"}
		return nil
	})
	res := &result{
		pat: &ts.PAT{TSID: 7, Programs: map[uint16]uint16{1: 0x30, 2: 0x40, 3: 0x50}},
		pmts: map[uint16]*ts.PMT{
			1: {Program: 1, PCRPID: 0x31, Streams: []ts.ESInfo{{PID: 0x31, Kind: "MPEG2VIDEO"}, {PID: 0x34, Kind: "AC3"}}},
			2: {Program: 2, Streams: []ts.ESInfo{{PID: 0x44, Kind: "AC3"}}},
			3: {Program: 3, Streams: []ts.ESInfo{{PID: 0x51, StreamType: 0x05}}},
		},
		vct: []ts.VCTChannel{
			{ShortName: "WTST-HD", Major: 7, Minor: 1, TSID: 7, Program: 1, ServiceType: 2, SourceID: 11},
			{ShortName: "WTST-FM", Major: 7, Minor: 2, TSID: 7, Program: 2, ServiceType: 2, SourceID: 12},
			{ShortName: "DATA", Major: 7, Minor: 9, TSID: 7, Program: 3, ServiceType: 4, Hidden: true},
		},
	}
	var found int
	st.Update(func(s *config.State) error { found = storeServices(s, s.Muxes["m"], res); return nil })
	st.View(func(s *config.State) {
		tv, radio, data := s.Services["m-1"], s.Services["m-2"], s.Services["m-3"]
		if tv.Name != "WTST-HD" || tv.Major != 7 || tv.Minor != 1 || tv.Kind != "tv" || tv.SourceID != 11 {
			t.Errorf("tv service %+v", tv)
		}
		if radio.Kind != "radio" {
			t.Errorf("audio-only service is %q, want radio", radio.Kind)
		}
		if data.Kind != "other" {
			t.Errorf("hidden data service is %q", data.Kind)
		}
	})
	if found != 2 {
		t.Errorf("found %d, want 2", found)
	}
}

// The same station on two frequencies becomes one channel with a backup,
// the better-received one first; numbers come from the broadcast.
func TestMapServicesMergesDuplicates(t *testing.T) {
	st := newStore(t)
	snr := func(v float64) *dvb.SignalSnap { return &dvb.SignalSnap{Bars: 4, SNRdB: &v} }
	st.Update(func(s *config.State) error {
		s.Muxes["weak"] = &config.Mux{ID: "weak", Signal: snr(14)}
		s.Muxes["strong"] = &config.Mux{ID: "strong", Signal: snr(28)}
		s.Services["a"] = &config.Service{ID: "a", MuxID: "weak", Name: "KABC", Major: 7, Minor: 1, Kind: "tv", Enabled: true}
		s.Services["b"] = &config.Service{ID: "b", MuxID: "strong", Name: "KABC", Major: 7, Minor: 1, Kind: "tv", Enabled: true}
		s.Services["c"] = &config.Service{ID: "c", MuxID: "strong", Name: "News", Kind: "tv", Enabled: true}
		s.Services["d"] = &config.Service{ID: "d", MuxID: "strong", Name: "Jazz", Kind: "radio", Enabled: true}
		return nil
	})
	created, merged := MapServices(st, DefaultMapOptions())
	if created != 2 || merged != 1 {
		t.Fatalf("created %d merged %d", created, merged)
	}
	st.View(func(s *config.State) {
		for _, c := range s.Channels {
			switch c.Name {
			case "KABC":
				if c.GuideNumber() != "7.1" || len(c.Services) != 2 || c.Services[0] != "b" {
					t.Errorf("KABC channel %+v", c)
				}
			case "News":
				if c.Number != 8 {
					t.Errorf("unnumbered service got %s, want the next free number 8", c.GuideNumber())
				}
			default:
				t.Errorf("unexpected channel %s (radio is off by default)", c.Name)
			}
		}
	})
	if c, m := MapServices(st, DefaultMapOptions()); c != 0 || m != 0 {
		t.Errorf("mapping twice created %d/%d more", c, m)
	}
}

func TestApplyPlanAddsOnlyMissing(t *testing.T) {
	st := newStore(t)
	st.Update(func(s *config.State) error { s.Networks["n"] = &config.Network{ID: "n", Type: "atsc"}; return nil })
	n, err := ApplyPlan(st, "n", "us-atsc")
	if err != nil || n != 35 {
		t.Fatalf("added %d (%v), want RF 2-36", n, err)
	}
	if n, _ := ApplyPlan(st, "n", "us-atsc"); n != 0 {
		t.Fatalf("second apply added %d", n)
	}
}
