package guide

import (
	"strings"
	"testing"
	"time"
)

func TestXMLTVImport(t *testing.T) {
	doc := `<?xml version="1.0"?><tv>
<channel id="kabc.us"><display-name>KABC</display-name><display-name>7.1</display-name></channel>
<programme start="20261001200000 -0400" stop="20261001210000 -0400" channel="kabc.us">
 <title>News at 8</title><sub-title>Tonight</sub-title><desc>Local news.</desc>
 <category>News</category><episode-num system="xmltv_ns">2.4.</episode-num></programme>
<programme start="20261001210000 -0400" stop="20261001220000 -0400" channel="other"><title>Skip</title></programme>
</tv>`
	events := map[string][]*Event{}
	chans, n, err := ParseXMLTV(strings.NewReader(doc), "test", map[string][]string{"kabc.us": {"ch1"}}, events)
	if err != nil || n != 1 || len(chans) != 1 {
		t.Fatalf("n=%d chans=%d err=%v", n, len(chans), err)
	}
	e := events["ch1"][0]
	if e.Title != "News at 8" || e.Season != 3 || e.Episode != 5 || e.EpisodeText != "S03E05" || !e.Start.Equal(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("event %+v", e)
	}
}

func TestPutReplacesOverlaps(t *testing.T) {
	g := &Guide{byChan: map[string][]*Event{}, dirty: make(chan struct{}, 1)}
	t0 := time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)
	g.Put("c", []*Event{{Start: t0, Stop: t0.Add(time.Hour), Title: "Old"}})
	g.Put("c", []*Event{{Start: t0.Add(30 * time.Minute), Stop: t0.Add(90 * time.Minute), Title: "New"}})
	evs := g.Range("c", t0, t0.Add(3*time.Hour))
	if len(evs) != 1 || evs[0].Title != "New" {
		t.Fatalf("events %+v", evs)
	}
	now, next := g.NowNext("c", t0.Add(45*time.Minute))
	if now == nil || now.Title != "New" || next != nil {
		t.Fatalf("now %v next %v", now, next)
	}
}

func TestNameMatching(t *testing.T) {
	if normName("BBC One HD") != normName("bbc-one") {
		t.Fatal("names should match ignoring case, punctuation and HD")
	}
}
