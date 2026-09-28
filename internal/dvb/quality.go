package dvb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dvbhub/internal/store"
)

// score is a 0-100 figure of merit: SNR if the driver reports it, else strength.
func (s Signal) score() float64 {
	switch {
	case s.SNRdB != nil:
		return *s.SNRdB * 100 / 30
	case s.SNRPct >= 0:
		return s.SNRPct
	}
	return s.StrengthPct
}

// Quality gives a one-word verdict: no-lock, poor, fair, good or excellent.
func (s Signal) Quality() string {
	if !s.Locked {
		return "no-lock"
	}
	sc := s.score()
	switch {
	case s.UNCDelta > 0 || s.BER > 1e-3:
		return "poor"
	case sc >= 0 && sc < 40:
		return "fair"
	case sc >= 0 && sc < 60:
		return "good"
	}
	return "excellent"
}

// Bars maps the signal onto 0-4 bars like a phone signal indicator.
func (s Signal) Bars() int {
	if !s.Locked {
		return 0
	}
	sc := s.score()
	bars := 4
	switch {
	case sc < 0:
		bars = 2 // locked but the driver reports nothing useful
	case sc < 40:
		bars = 1
	case sc < 55:
		bars = 2
	case sc < 70:
		bars = 3
	}
	if (s.UNCDelta > 0 || s.BER > 1e-3) && bars > 1 {
		bars = 1
	}
	return bars
}

// Snapshot converts a live reading to the persisted form.
func (s Signal) Snapshot() store.SignalSnap {
	return store.SignalSnap{Locked: s.Locked, StrengthPct: s.StrengthPct, StrengthDBm: s.StrengthDBm, SNRdB: s.SNRdB,
		SNRPct: s.SNRPct, Bars: s.Bars(), Quality: s.Quality(), At: time.Now()}
}

// Proc is a process holding a DVB device open.
type Proc struct {
	PID  int    `json:"pid"`
	Name string `json:"name"`
}

var (
	usersMu    sync.Mutex
	usersCache map[int][]Proc
	usersAt    time.Time
)

// DeviceUsers lists other processes that have /dev/dvb/adapterN/* open.
// It only sees processes this user may inspect (all of them when root,
// none outside the container when running in Docker). Cached for 5 s.
func DeviceUsers(adapter int) []Proc {
	usersMu.Lock()
	defer usersMu.Unlock()
	if usersCache == nil || time.Since(usersAt) > 5*time.Second {
		usersCache = scanDeviceUsers()
		usersAt = time.Now()
	}
	return usersCache[adapter]
}

func scanDeviceUsers() map[int][]Proc {
	out := map[int][]Proc{}
	self := os.Getpid()
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil || pid == self {
			continue
		}
		fds, err := os.ReadDir(filepath.Join("/proc", d.Name(), "fd"))
		if err != nil {
			continue
		}
		seen := map[int]bool{}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join("/proc", d.Name(), "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(target, "/dev/dvb/adapter") {
				continue
			}
			var a int
			if _, err := fmt.Sscanf(target, "/dev/dvb/adapter%d/", &a); err == nil && !seen[a] {
				seen[a] = true
				name, _ := os.ReadFile(filepath.Join("/proc", d.Name(), "comm"))
				out[a] = append(out[a], Proc{PID: pid, Name: strings.TrimSpace(string(name))})
			}
		}
	}
	for a := range out {
		sort.Slice(out[a], func(i, j int) bool { return out[a][i].PID < out[a][j].PID })
	}
	return out
}

// DescribeUsers formats device users for error messages.
func DescribeUsers(adapter int) string {
	users := DeviceUsers(adapter)
	if len(users) == 0 {
		return "another program (not visible from here)"
	}
	var parts []string
	for _, u := range users {
		parts = append(parts, fmt.Sprintf("%s (pid %d)", u.Name, u.PID))
	}
	return strings.Join(parts, ", ")
}
