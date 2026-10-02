package mpegts

import (
	"strings"
	"testing"
	"time"
)

func collect(t *testing.T, pid uint16, pkts []byte) [][]byte {
	t.Helper()
	a := NewSectionAssembler()
	var out [][]byte
	ForEach(pkts, func(p []byte) {
		if PID(p) == pid {
			a.Push(p, func(s []byte) { out = append(out, s) })
		}
	})
	return out
}

func TestPATRoundTrip(t *testing.T) {
	var cc byte
	pkts := Packetize(0, BuildPAT(0x1234, 3, map[uint16]uint16{101: 0x100, 102: 0x200}), &cc)
	secs := collect(t, 0, pkts)
	if len(secs) != 1 {
		t.Fatalf("got %d sections", len(secs))
	}
	s, err := ParseSection(secs[0])
	if err != nil {
		t.Fatal(err)
	}
	pat, err := ParsePAT(s)
	if err != nil {
		t.Fatal(err)
	}
	if pat.TSID != 0x1234 || pat.Version != 3 || pat.Programs[101] != 0x100 || pat.Programs[102] != 0x200 {
		t.Fatalf("bad PAT %+v", pat)
	}
}

func TestMultiPacketEIT(t *testing.T) {
	start := time.Date(2026, 9, 27, 18, 30, 0, 0, time.UTC)
	var evs []Event
	for i := 0; i < 6; i++ {
		evs = append(evs, Event{
			EventID: uint16(i + 1), Start: start.Add(time.Duration(i) * time.Hour), Duration: 45 * time.Minute,
			Title: "Programme", Subtitle: "Episode synopsis", Description: strings.Repeat("Long text. ", 20),
			Genres: []byte{0x23},
		})
	}
	var cc byte
	sec := BuildEIT(0x50, 101, 1, 2, 5, 0, 0, evs)
	pkts := Packetize(PIDEIT, sec, &cc)
	if len(pkts) < 3*PacketSize {
		t.Fatalf("expected multi-packet section, got %d bytes", len(pkts))
	}
	secs := collect(t, PIDEIT, pkts)
	if len(secs) != 1 {
		t.Fatalf("got %d sections", len(secs))
	}
	s, err := ParseSection(secs[0])
	if err != nil {
		t.Fatal(err)
	}
	e, err := ParseEIT(s)
	if err != nil {
		t.Fatal(err)
	}
	if e.SID != 101 || e.TSID != 1 || e.ONID != 2 || len(e.Events) != 6 {
		t.Fatalf("bad EIT %+v", e)
	}
	ev := e.Events[3]
	if !ev.Start.Equal(start.Add(3*time.Hour)) || ev.Duration != 45*time.Minute || ev.Title != "Programme" ||
		ev.Subtitle != "Episode synopsis" || !strings.HasPrefix(ev.Description, "Long text.") || GenreName(ev.Genres[0]) != "News / Current affairs" {
		t.Fatalf("bad event %+v", ev)
	}
}

func TestSDTAndNIT(t *testing.T) {
	sdt := BuildSDT(7, 0x233a, 1, []SDTService{{SID: 101, Type: 0x19, Provider: "Prov", Name: "News HD", EITPF: true}})
	s, err := ParseSection(sdt)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseSDT(s)
	if err != nil {
		t.Fatal(err)
	}
	if p.ONID != 0x233a || p.TSID != 7 || len(p.Services) != 1 || p.Services[0].Name != "News HD" || ServiceKind(p.Services[0].Type) != "tv" {
		t.Fatalf("bad SDT %+v", p)
	}
	nit := BuildNIT(0x3005, 0, "Testnet", []NITTransport{{TSID: 7, ONID: 0x233a,
		Delivery: &Delivery{FrequencyKHz: 506000}, LCN: map[uint16]int{101: 1, 102: 22}}})
	s, err = ParseSection(nit)
	if err != nil {
		t.Fatal(err)
	}
	n, err := ParseNIT(s)
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "Testnet" || len(n.Transports) != 1 {
		t.Fatalf("bad NIT %+v", n)
	}
	tr := n.Transports[0]
	if tr.Delivery == nil || tr.Delivery.FrequencyKHz != 506000 || tr.Delivery.BandwidthHz != 8e6 || tr.LCN[102] != 22 {
		t.Fatalf("bad transport %+v %+v", tr, tr.Delivery)
	}
}

func TestSectionAcrossPacketBoundaryWithFollowingSection(t *testing.T) {
	// Two PATs back to back in one payload stream exercise pointer_field handling.
	a := BuildPAT(1, 0, map[uint16]uint16{1: 0x100})
	b := BuildPAT(1, 1, map[uint16]uint16{2: 0x101})
	var cc byte
	pkts := append(Packetize(0, a, &cc), Packetize(0, b, &cc)...)
	secs := collect(t, 0, pkts)
	if len(secs) != 2 {
		t.Fatalf("got %d sections", len(secs))
	}
}

func TestDecodeText(t *testing.T) {
	cases := map[string]string{
		"Plain":                  "Plain",
		"\xc2e":                  "é",
		"\x15Grüße":              "Grüße",
		"\x05\xfe":               "ş",
		"\x11\x00A\x00B":         "AB",
		"Line\x8anext":           "Line\nnext",
		"\x10\x00\x0f\xa4 price": "€ price",
	}
	for in, want := range cases {
		if got := DecodeText([]byte(in)); got != want {
			t.Errorf("DecodeText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAligner(t *testing.T) {
	var cc byte
	pkts := Packetize(0, BuildPAT(1, 0, map[uint16]uint16{1: 0x100}), &cc)
	var a Aligner
	junk := append([]byte{1, 2, 3}, pkts...)
	out := a.Feed(junk[:50])
	out = append(out, a.Feed(junk[50:])...)
	if len(out) != PacketSize {
		t.Fatalf("aligned %d bytes", len(out))
	}
}

func TestVCTRoundTrip(t *testing.T) {
	in := []VCTChannel{
		{ShortName: "WSAV-HD", Major: 3, Minor: 1, TSID: 0x0123, Program: 3, ServiceType: 0x02},
		{ShortName: "Court", Major: 3, Minor: 4, TSID: 0x0123, Program: 4, ServiceType: 0x02, AccessControlled: true},
		{ShortName: "", Major: 1000, Minor: 999, TSID: 0x0123, Program: 5, ServiceType: 0x04, Hidden: true},
	}
	sec, err := ParseSection(BuildVCT(0x0123, 1, in))
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseVCT(sec)
	if err != nil {
		t.Fatal(err)
	}
	if v.TSID != 0x0123 || len(v.Channels) != len(in) {
		t.Fatalf("got %+v", v)
	}
	for i := range in {
		if v.Channels[i] != in[i] {
			t.Errorf("channel %d: got %+v want %+v", i, v.Channels[i], in[i])
		}
	}
}

// Hand-built TVCT bytes for "KSMO" 62.1, program 3, laid out per the A/65
// field offsets, independent of BuildVCT.
func TestVCTFieldOffsets(t *testing.T) {
	c := make([]byte, 32)
	copy(c, []byte{0, 'K', 0, 'S', 0, 'M', 0, 'O', 0, 0, 0, 0, 0, 0})
	c[14], c[15], c[16] = 0xf0, 62<<2, 1 // major 62, minor 1
	c[24], c[25] = 0x00, 0x03
	c[26], c[27] = 0x0d, 0xc2
	c[30], c[31] = 0xfc, 0x00
	data := append([]byte{0, 1}, c...)
	data = append(data, 0xfc, 0x00)
	sec, _ := ParseSection(BuildSection(0xc8, 7, 0, 0, 0, data))
	v, err := ParseVCT(sec)
	if err != nil || len(v.Channels) != 1 {
		t.Fatal(err)
	}
	ch := v.Channels[0]
	if ch.ShortName != "KSMO" || ch.Major != 62 || ch.Minor != 1 || ch.Program != 3 || ch.ServiceType != 2 {
		t.Fatalf("got %+v", ch)
	}
}

func TestATSCGuideTables(t *testing.T) {
	sec := func(b []byte) *Section {
		t.Helper()
		s, err := ParseSection(b)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	mgt, err := ParseMGT(sec(BuildMGT([]MGTTable{{Type: 0x0000, PID: 0x1ffb}, {Type: 0x0100, PID: 0x1d00, Version: 3}, {Type: 0x0200, PID: 0x1e00}})))
	if err != nil || len(mgt) != 3 || mgt[1].PID != 0x1d00 || mgt[1].Version != 3 || mgt[0].IsGuide() || !mgt[1].IsGuide() || !mgt[2].IsGuide() {
		t.Fatalf("mgt %+v %v", mgt, err)
	}
	now := time.Date(2026, 9, 27, 19, 30, 0, 0, time.UTC)
	if off, err := ParseSTT(sec(BuildSTT(now, 18))); err != nil || off != 18 {
		t.Fatalf("stt %d %v", off, err)
	}
	if got := GPSTime(GPSSeconds(now, 18), 18); !got.Equal(now) {
		t.Fatalf("gps round trip %v", got)
	}
	// 2026-09-27 19:30 UTC is 1,474,572,618 GPS seconds (18 leap seconds).
	if s := GPSSeconds(now, 18); s != 1474572618 {
		t.Fatalf("gps seconds %d", s)
	}
	evs := []ATSCEvent{{EventID: 7, Start: GPSSeconds(now, 18), Duration: 30 * time.Minute, Title: "News at 3"},
		{EventID: 8, Start: GPSSeconds(now.Add(30*time.Minute), 18), Duration: 90 * time.Minute, Title: "Movie"}}
	eit, err := ParseATSCEIT(sec(BuildATSCEIT(1003, 1, evs)))
	if err != nil || eit.SourceID != 1003 || len(eit.Events) != 2 || eit.Events[1] != evs[1] || eit.Events[0] != evs[0] {
		t.Fatalf("eit %+v %v", eit, err)
	}
	ett, err := ParseETT(sec(BuildETT(1003, 8, 0, "A film.")))
	if err != nil || ett.SourceID != 1003 || ett.EventID != 8 || ett.Text != "A film." {
		t.Fatalf("ett %+v %v", ett, err)
	}
	// Two languages, the English one second; UTF-16 mode.
	b := []byte{2, 's', 'p', 'a', 1, 0, 0, 4, 'H', 'o', 'l', 'a', 'e', 'n', 'g', 1, 0, 0x3f, 4, 0, 'H', 0, 'i'}
	if got := DecodeMSS(b); got != "Hi" {
		t.Fatalf("mss %q", got)
	}
}
