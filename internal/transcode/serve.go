package transcode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"dvbhub/internal/config"
	ts "dvbhub/internal/mpegts"
	"dvbhub/internal/tuners"
)

// Options are the host-wide transcoding settings.
type Options struct {
	FFmpeg      string
	DefaultKind string // Settings.HWAccel: auto, nvenc, qsv, vaapi, cpu
	Caps        Caps
}

// NeedsFFmpeg reports whether a profile re-encodes a service with this video codec.
func NeedsFFmpeg(p *config.Profile, videoCodec string) bool {
	switch {
	case p.Passthrough():
		return false
	case p.Mode == "mpeg2":
		return videoCodec == "MPEG2VIDEO"
	}
	return true
}

// Serve copies sub to w, passed through or transcoded, until ctx ends or the
// subscription ends. flush is called after each write burst.
func Serve(ctx context.Context, sub *tuners.Subscription, p *config.Profile, opt Options, w io.Writer, flush func()) error {
	codec := ""
	if svc := sub.Service(); svc != nil {
		codec = svc.VideoCodec()
	}
	if p == nil || !NeedsFFmpeg(p, codec) {
		return passthrough(ctx, sub, w, flush)
	}
	return transcode(ctx, sub, p, codec, opt, w, flush)
}

func passthrough(ctx context.Context, sub *tuners.Subscription, w io.Writer, flush func()) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sub.Done:
			return sub.Err()
		case b := <-sub.C:
			if _, err := w.Write(b); err != nil {
				return err
			}
			// Coalesce whatever else is queued before flushing.
			for more := true; more; {
				select {
				case b := <-sub.C:
					if _, err := w.Write(b); err != nil {
						return err
					}
				default:
					more = false
				}
			}
			if flush != nil {
				flush()
			}
		}
	}
}

// Status is live transcoder state, shown with the subscription in the UI.
type Status struct {
	mu sync.Mutex
	StatusInfo
}

type StatusInfo struct {
	Profile   string  `json:"profile"`
	Kind      string  `json:"kind"`
	Encoder   string  `json:"encoder"`
	HWDecode  bool    `json:"hwDecode"`
	FPS       float64 `json:"fps"`
	Speed     string  `json:"speed"`
	OutKbps   float64 `json:"outKbps"`
	TargetK   int     `json:"targetKbps"`
	Restarts  int     `json:"restarts"`
	Fallbacks int     `json:"fallbacks"`
	LastError string  `json:"lastError,omitempty"`
}

func (s *Status) set(fn func(*StatusInfo)) {
	s.mu.Lock()
	fn(&s.StatusInfo)
	s.mu.Unlock()
}

type statusView struct{ s *Status }

func (v statusView) MarshalJSON() ([]byte, error) {
	v.s.mu.Lock()
	defer v.s.mu.Unlock()
	return json.Marshal(v.s.StatusInfo)
}

// errClient means the client went away.
type errClient struct{ err error }

func (e *errClient) Error() string { return "client write: " + e.err.Error() }

func transcode(ctx context.Context, sub *tuners.Subscription, p *config.Profile, codec string, opt Options, w io.Writer, flush func()) error {
	plans := Plans(p, opt.DefaultKind, &opt.Caps)
	st := &Status{StatusInfo: StatusInfo{Profile: p.Name, TargetK: p.BitrateK}}
	sub.SetTranscodeStatus(statusView{st})
	attempt := 0
	var restarts []time.Time
	for {
		plan := plans[attempt]
		st.set(func(s *StatusInfo) { s.Kind, s.Encoder, s.HWDecode = plan.Kind, plan.Encoder, plan.HWDecode })
		started := time.Now()
		out, err := runFFmpeg(ctx, sub, p, plan, codec, opt, w, flush, st)
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-sub.Done:
			return sub.Err()
		default:
		}
		var ce *errClient
		if errors.As(err, &ce) {
			return ce.err
		}
		msg := "ffmpeg ended"
		if err != nil {
			msg = err.Error()
		}
		st.set(func(s *StatusInfo) { s.LastError = msg })
		// A pipeline that dies at once without output can't work here (no
		// GPU, unsupported codec, missing filter): try the next plan.
		if out == 0 && time.Since(started) < 15*time.Second && attempt+1 < len(plans) {
			attempt++
			next := plans[attempt]
			log.Printf("transcode: %s with %s failed (%s); trying %s", p.Name, describe(plan), msg, describe(next))
			st.set(func(s *StatusInfo) { s.Fallbacks++ })
		} else {
			log.Printf("transcode: ffmpeg for %s exited (%s); restarting", p.Name, msg)
			st.set(func(s *StatusInfo) { s.Restarts++ })
			now := time.Now()
			restarts = append(restarts, now)
			for len(restarts) > 0 && now.Sub(restarts[0]) > time.Minute {
				restarts = restarts[1:]
			}
			if len(restarts) > 6 {
				return fmt.Errorf("ffmpeg keeps failing: %s", msg)
			}
		}
		// Keep the client connected while ffmpeg restarts.
		if _, err := w.Write(tuners.NullPackets(20)); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func describe(p Plan) string {
	s := p.Encoder
	if p.HWDecode {
		s += " (GPU decode)"
	}
	return s
}

func runFFmpeg(ctx context.Context, sub *tuners.Subscription, p *config.Profile, plan Plan, codec string, opt Options,
	w io.Writer, flush func(), st *Status) (int64, error) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(cctx, opt.FFmpeg, Args(p, plan, codec, opt.Caps.Filters)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	progR, progW, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	cmd.ExtraFiles = []*os.File{progW} // fd 3 for -progress
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		progR.Close()
		progW.Close()
		return 0, fmt.Errorf("starting %s: %w", opt.FFmpeg, err)
	}
	progW.Close()
	go readProgress(progR, st)

	// Feed the subscription into ffmpeg.
	go func() {
		defer stdin.Close()
		for {
			select {
			case <-cctx.Done():
				return
			case <-sub.Done:
				cancel()
				return
			case b := <-sub.C:
				if _, err := stdin.Write(b); err != nil {
					return
				}
			}
		}
	}()

	// Read ffmpeg's output as whole packets so keep-alive nulls can be interleaved.
	chunks := make(chan []byte, 64)
	readErr := make(chan error, 1)
	go func() {
		var al ts.Aligner
		buf := make([]byte, 64*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if pk := al.Feed(buf[:n]); len(pk) > 0 {
					select {
					case chunks <- pk:
					case <-cctx.Done():
						return
					}
				}
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	var total int64
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	lastOut := time.Now()
	var winBytes int
	winStart := time.Now()
	fail := func(err error) (int64, error) {
		cancel()
		cmd.Wait()
		return total, err
	}
	for {
		select {
		case <-ctx.Done():
			return fail(nil)
		case b := <-chunks:
			if _, err := w.Write(b); err != nil {
				return fail(&errClient{err})
			}
			if flush != nil {
				flush()
			}
			total += int64(len(b))
			lastOut = time.Now()
			winBytes += len(b)
		case <-tick.C:
			if el := time.Since(winStart); el >= 2*time.Second {
				kbps := float64(winBytes) * 8 / 1000 / el.Seconds()
				st.set(func(s *StatusInfo) { s.OutKbps = kbps })
				winBytes, winStart = 0, time.Now()
			}
			if time.Since(lastOut) > 1500*time.Millisecond {
				// No output (tuner outage or a stalled encoder): keep the client alive.
				if _, err := w.Write(tuners.NullPackets(10)); err != nil {
					return fail(&errClient{err})
				}
				if flush != nil {
					flush()
				}
			}
		case err := <-readErr:
			for more := true; more; {
				select {
				case b := <-chunks:
					if _, werr := w.Write(b); werr == nil {
						total += int64(len(b))
					}
				default:
					more = false
				}
			}
			werr := cmd.Wait()
			if werr == nil && err != io.EOF {
				werr = err
			}
			if werr != nil {
				return total, fmt.Errorf("%v: %s", werr, stderr.String())
			}
			return total, errors.New("ffmpeg ended")
		}
	}
}

func readProgress(r io.ReadCloser, st *Status) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "fps":
			f, _ := strconv.ParseFloat(v, 64)
			st.set(func(s *StatusInfo) { s.FPS = f })
		case "speed":
			st.set(func(s *StatusInfo) { s.Speed = strings.TrimSpace(v) })
		}
	}
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return lastLines(string(t.b), 3)
}
