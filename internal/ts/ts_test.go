package ts

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
