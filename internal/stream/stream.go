// Package stream writes a subscription to a client, either passed through
// or transcoded with ffmpeg (NVENC/NVDEC or CPU), keeping the output alive
// across tuner outages and ffmpeg restarts.
package stream

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

	"dvbhub/internal/store"
	"dvbhub/internal/ts"
	"dvbhub/internal/tuner"
)

// IsPassthrough reports whether a profile needs no ffmpeg at all.
func IsPassthrough(p *store.Profile) bool {
	return p == nil || ((p.VideoCodec == "" || p.VideoCodec == "copy") && (p.AudioCodec == "" || p.AudioCodec == "copy"))
}

// Serve copies sub to w until ctx ends or the subscription ends.
func Serve(ctx context.Context, sub *tuner.Subscription, p *store.Profile, ffmpeg string, w io.Writer, flush func()) error {
	if IsPassthrough(p) {
		return passthrough(ctx, sub, w, flush)
	}
	return transcode(ctx, sub, p, ffmpeg, w, flush)
}

func passthrough(ctx context.Context, sub *tuner.Subscription, w io.Writer, flush func()) error {
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

// Status is live transcoder state shown in the UI.
type Status struct {
	mu sync.Mutex
	StatusInfo
}

type StatusInfo struct {
	Profile   string  `json:"profile"`
	Encoder   string  `json:"encoder"`
	HWDecode  bool    `json:"hwDecode"`
	FPS       float64 `json:"fps"`
	Speed     string  `json:"speed"`
	OutKbps   float64 `json:"outKbps"`
	TargetK   int     `json:"targetKbps"`
	Restarts  int     `json:"restarts"`
	LastError string  `json:"lastError,omitempty"`
}

func (s *Status) snapshot() StatusInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.StatusInfo
}

type statusView struct{ s *Status }

func (v statusView) MarshalJSON() ([]byte, error) { return json.Marshal(v.s.snapshot()) }

func transcode(ctx context.Context, sub *tuner.Subscription, p *store.Profile, ffmpeg string, w io.Writer, flush func()) error {
	st := &Status{StatusInfo: StatusInfo{Profile: p.Name, Encoder: p.VideoCodec, HWDecode: p.HWDecode, TargetK: p.BitrateK}}
	sub.SetTranscodeStatus(statusView{st})
	prof := *p
	var restarts []time.Time
	for {
		started := time.Now()
		err := runFFmpeg(ctx, sub, &prof, ffmpeg, w, flush, st)
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-sub.Done:
			return sub.Err()
		default:
		}
		var werr *writeError
		if errors.As(err, &werr) {
			return werr.err // client went away
		}
		st.mu.Lock()
		st.Restarts++
		if err != nil {
			st.LastError = err.Error()
		}
		st.mu.Unlock()
		// Hardware decode can fail for some codecs/profiles; fall back to CPU decode.
		if prof.HWDecode && time.Since(started) < 10*time.Second {
			log.Printf("transcode: ffmpeg failed quickly with NVDEC (%v); retrying with CPU decode", err)
			prof.HWDecode = false
			st.mu.Lock()
			st.HWDecode = false
			st.mu.Unlock()
		} else {
			log.Printf("transcode: ffmpeg exited (%v); restarting", err)
		}
		now := time.Now()
		restarts = append(restarts, now)
		for len(restarts) > 0 && now.Sub(restarts[0]) > time.Minute {
			restarts = restarts[1:]
		}
		if len(restarts) > 6 {
			return fmt.Errorf("ffmpeg keeps failing: %v", err)
		}
		// Keep the client connection alive while we restart.
		if _, err := w.Write(tuner.NullPackets(20)); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

type writeError struct{ err error }

func (e *writeError) Error() string { return "client write: " + e.err.Error() }

func runFFmpeg(ctx context.Context, sub *tuner.Subscription, p *store.Profile, ffmpeg string, w io.Writer, flush func(), st *Status) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := BuildArgs(p)
	cmd := exec.CommandContext(cctx, ffmpeg, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	progR, progW, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.ExtraFiles = []*os.File{progW} // fd 3 for -progress
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		progR.Close()
		progW.Close()
		return fmt.Errorf("starting %s: %w", ffmpeg, err)
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

	// Read ffmpeg output as whole packets so keep-alive nulls can be interleaved.
	chunks := make(chan []byte, 64)
	readErr := make(chan error, 1)
	go func() {
		var al ts.Aligner
		buf := make([]byte, 64*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if pk := al.Feed(buf[:n]); len(pk) > 0 {
					chunks <- pk
				}
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	lastOut := time.Now()
	var winBytes int
	winStart := time.Now()
	for {
		select {
		case <-ctx.Done():
			cmd.Wait()
			return nil
		case b := <-chunks:
			if _, err := w.Write(b); err != nil {
				cancel()
				cmd.Wait()
				return &writeError{err}
			}
			if flush != nil {
				flush()
			}
			lastOut = time.Now()
			winBytes += len(b)
		case <-tick.C:
			if el := time.Since(winStart); el >= 2*time.Second {
				st.mu.Lock()
				st.OutKbps = float64(winBytes) * 8 / 1000 / el.Seconds()
				st.mu.Unlock()
				winBytes, winStart = 0, time.Now()
			}
			if time.Since(lastOut) > 1500*time.Millisecond {
				// No output (tuner outage or encoder stall): keep the client alive.
				if _, err := w.Write(tuner.NullPackets(10)); err != nil {
					cancel()
					cmd.Wait()
					return &writeError{err}
				}
				if flush != nil {
					flush()
				}
			}
		case err := <-readErr:
			for more := true; more; { // drain
				select {
				case b := <-chunks:
					w.Write(b)
				default:
					more = false
				}
			}
			werr := cmd.Wait()
			if werr == nil && err != io.EOF {
				werr = err
			}
			if werr != nil {
				return fmt.Errorf("%v: %s", werr, stderr.String())
			}
			return errors.New("ffmpeg ended")
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
		st.mu.Lock()
		switch k {
		case "fps":
			st.FPS, _ = strconv.ParseFloat(v, 64)
		case "speed":
			st.Speed = strings.TrimSpace(v)
		}
		st.mu.Unlock()
	}
}

// tailBuffer keeps the last max bytes written.
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
	s := strings.TrimSpace(string(t.b))
	if i := strings.LastIndex(s, "\n"); i >= 0 && len(s)-i < 300 {
		// Prefer the last few lines.
		lines := strings.Split(s, "\n")
		if len(lines) > 3 {
			lines = lines[len(lines)-3:]
		}
		s = strings.Join(lines, " | ")
	}
	return s
}

func isNVENC(codec string) bool { return strings.HasSuffix(codec, "_nvenc") }

// BuildArgs returns the ffmpeg command line (without the binary) for a profile.
func BuildArgs(p *store.Profile) []string {
	a := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-progress", "pipe:3", "-stats_period", "2"}
	vcopy := p.VideoCodec == "" || p.VideoCodec == "copy"
	hw := p.HWDecode && isNVENC(p.VideoCodec)
	if hw {
		a = append(a, "-hwaccel", "cuda", "-hwaccel_output_format", "cuda", "-hwaccel_device", strconv.Itoa(p.GPU))
	}
	a = append(a, "-fflags", "+genpts+discardcorrupt", "-err_detect", "ignore_err",
		"-analyzeduration", "2000000", "-probesize", "4000000", "-thread_queue_size", "1024",
		"-f", "mpegts", "-i", "pipe:0",
		"-map", "0:v:0?", "-map", "0:a?", "-sn", "-dn", "-ignore_unknown")

	if vcopy {
		a = append(a, "-c:v", "copy")
	} else {
		var filters []string
		if hw {
			if p.Deinterlace {
				filters = append(filters, "yadif_cuda=0:-1:1")
			}
			if p.Height > 0 {
				filters = append(filters, fmt.Sprintf("scale_cuda=-2:%d", p.Height))
			}
		} else {
			if p.Deinterlace {
				filters = append(filters, "yadif=0:-1:1")
			}
			if p.Height > 0 {
				filters = append(filters, fmt.Sprintf("scale=-2:%d", p.Height))
			}
			filters = append(filters, "format=yuv420p")
		}
		if len(filters) > 0 {
			a = append(a, "-vf", strings.Join(filters, ","))
		}
		if p.FPS > 0 {
			a = append(a, "-r", strconv.Itoa(p.FPS))
		}
		a = append(a, "-c:v", p.VideoCodec)
		if isNVENC(p.VideoCodec) {
			if p.Preset != "" {
				a = append(a, "-preset", p.Preset)
			}
			a = append(a, "-gpu", strconv.Itoa(p.GPU))
			switch p.RateControl {
			case "cbr":
				a = append(a, "-rc", "cbr", "-b:v", kb(p.BitrateK), "-maxrate", kb(p.BitrateK))
			case "cq":
				a = append(a, "-rc", "vbr", "-cq", strconv.Itoa(max(p.CQ, 1)), "-b:v", "0")
				if p.MaxrateK > 0 {
					a = append(a, "-maxrate", kb(p.MaxrateK))
				}
			default:
				a = append(a, "-rc", "vbr", "-b:v", kb(p.BitrateK))
				if p.MaxrateK > 0 {
					a = append(a, "-maxrate", kb(p.MaxrateK))
				}
			}
		} else {
			if p.Preset != "" {
				a = append(a, "-preset", p.Preset)
			}
			switch p.RateControl {
			case "cbr":
				a = append(a, "-b:v", kb(p.BitrateK), "-minrate", kb(p.BitrateK), "-maxrate", kb(p.BitrateK))
				if p.VideoCodec == "libx264" {
					a = append(a, "-x264-params", "nal-hrd=cbr")
				}
			case "cq":
				a = append(a, "-crf", strconv.Itoa(max(p.CQ, 1)))
				if p.MaxrateK > 0 {
					a = append(a, "-maxrate", kb(p.MaxrateK))
				}
			default:
				a = append(a, "-b:v", kb(p.BitrateK))
				if p.MaxrateK > 0 {
					a = append(a, "-maxrate", kb(p.MaxrateK))
				}
			}
		}
		if p.BufsizeK > 0 {
			a = append(a, "-bufsize", kb(p.BufsizeK))
		} else if p.RateControl != "cq" && p.BitrateK > 0 {
			a = append(a, "-bufsize", kb(p.BitrateK*2))
		}
		gop := 50
		if p.FPS > 0 {
			gop = p.FPS * 2
		}
		a = append(a, "-g", strconv.Itoa(gop))
	}

	switch p.AudioCodec {
	case "", "copy":
		a = append(a, "-c:a", "copy")
	default:
		a = append(a, "-c:a", p.AudioCodec)
		if p.AudioK > 0 {
			a = append(a, "-b:a", kb(p.AudioK))
		}
		if p.AudioCh > 0 {
			a = append(a, "-ac", strconv.Itoa(p.AudioCh))
		}
	}
	if p.Extra != "" {
		a = append(a, strings.Fields(p.Extra)...)
	}
	a = append(a, "-max_muxing_queue_size", "4096", "-f", "mpegts", "-mpegts_flags", "+resend_headers",
		"-muxdelay", "0", "-muxpreload", "0", "pipe:1")
	return a
}

func kb(v int) string { return strconv.Itoa(v) + "k" }
