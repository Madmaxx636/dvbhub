package server

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LogBuffer keeps the last lines written to the log so the UI can show them.
type LogBuffer struct {
	mu    sync.Mutex
	lines []LogLine
	next  int64
	part  string
}

type LogLine struct {
	N    int64     `json:"n"`
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

const logKeep = 1000

func NewLogBuffer() *LogBuffer { return &LogBuffer{} }

func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.part + string(p)
	parts := strings.Split(s, "\n")
	b.part = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		if l == "" {
			continue
		}
		b.next++
		b.lines = append(b.lines, LogLine{N: b.next, Time: time.Now(), Text: l})
	}
	if len(b.lines) > logKeep {
		b.lines = append([]LogLine(nil), b.lines[len(b.lines)-logKeep:]...)
	}
	return len(p), nil
}

// Since returns lines numbered after n.
func (b *LogBuffer) Since(n int64) []LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []LogLine{}
	for _, l := range b.lines {
		if l.N > n {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) apiLogs(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if s.Logs == nil {
		writeJSON(w, []LogLine{})
		return
	}
	writeJSON(w, s.Logs.Since(n))
}
