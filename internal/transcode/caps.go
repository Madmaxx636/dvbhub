// Package transcode delivers a subscription to a client: passed through
// untouched, or re-encoded with ffmpeg on an NVIDIA (NVENC), Intel (QSV),
// AMD/Intel (VAAPI) GPU or the CPU. Hardware is detected and test-encoded
// once, and a failing GPU pipeline falls back to the CPU automatically.
package transcode

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kinds of encoder, in the order "auto" prefers them.
var Kinds = []string{"nvenc", "qsv", "vaapi", "cpu"}

// Codecs that profiles can produce.
var Codecs = []string{"h264", "hevc", "av1"}

// EncoderName is ffmpeg's encoder for a kind and codec.
func EncoderName(kind, codec string) string {
	switch kind {
	case "nvenc", "qsv", "vaapi":
		return codec + "_" + kind
	}
	switch codec {
	case "hevc":
		return "libx265"
	case "av1":
		return "libsvtav1"
	}
	return "libx264"
}

// Encoder is one encoder and whether it works on this machine.
type Encoder struct {
	Kind     string `json:"kind"`
	Codec    string `json:"codec"`
	Name     string `json:"name"`
	InFFmpeg bool   `json:"inFfmpeg"` // compiled into ffmpeg
	Tested   bool   `json:"tested"`
	Works    bool   `json:"works"`
	Error    string `json:"error,omitempty"`
	HWDecode bool   `json:"hwDecode"` // the matching hardware decoder is available
}

// GPU is an NVIDIA GPU as reported by nvidia-smi.
type GPU struct {
	Index      int     `json:"index"`
	Name       string  `json:"name"`
	Util       float64 `json:"util"`
	EncUtil    float64 `json:"encUtil"`
	DecUtil    float64 `json:"decUtil"`
	MemUsedMB  float64 `json:"memUsedMb"`
	MemTotalMB float64 `json:"memTotalMb"`
	TempC      float64 `json:"tempC"`
	Sessions   int     `json:"encoderSessions"`
}

// RenderNode is a /dev/dri render device (Intel/AMD GPUs for QSV/VAAPI).
type RenderNode struct {
	Path   string `json:"path"`
	Vendor string `json:"vendor"` // intel, amd, nvidia, other
	Driver string `json:"driver,omitempty"`
}

// Caps describes what this host can transcode with.
type Caps struct {
	FFmpeg      string            `json:"ffmpeg"`
	FFmpegOK    bool              `json:"ffmpegOk"`
	Version     string            `json:"version"`
	HWAccels    []string          `json:"hwaccels"`
	Filters     map[string]bool   `json:"filters"`
	Encoders    []Encoder         `json:"encoders"`
	GPUs        []GPU             `json:"gpus"`
	SMIError    string            `json:"smiError,omitempty"`
	RenderNodes []RenderNode      `json:"renderNodes"`
	Probing     bool              `json:"probing"`
	ProbedAt    time.Time         `json:"probedAt"`
	Auto        map[string]string `json:"auto"` // codec -> kind that "auto" uses
}

// Encoder returns the entry for kind/codec.
func (c *Caps) Encoder(kind, codec string) Encoder {
	for _, e := range c.Encoders {
		if e.Kind == kind && e.Codec == codec {
			return e
		}
	}
	return Encoder{Kind: kind, Codec: codec, Name: EncoderName(kind, codec)}
}

// Best returns the kind "auto" should use for a codec: the first working
// hardware encoder, else the CPU.
func (c *Caps) Best(codec string) string {
	for _, k := range Kinds {
		if e := c.Encoder(k, codec); e.Works {
			return k
		}
	}
	return "cpu"
}

// Detector probes ffmpeg and the GPUs and caches the result.
type Detector struct {
	mu      sync.Mutex
	caps    *Caps
	gpuAt   time.Time
	probing bool
}

func NewDetector() *Detector { return &Detector{} }

func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// Caps returns the last detection (detecting ffmpeg's features now if
// needed); GPU load is refreshed at most every 2 seconds.
func (d *Detector) Caps(ffmpeg string) Caps {
	d.mu.Lock()
	if d.caps == nil || d.caps.FFmpeg != ffmpeg {
		d.caps = detectFeatures(ffmpeg)
	}
	if time.Since(d.gpuAt) > 2*time.Second {
		d.gpuAt = time.Now()
		d.caps.GPUs, d.caps.SMIError = queryGPUs()
	}
	c := *d.caps
	c.Probing = d.probing
	c.Encoders = append([]Encoder(nil), d.caps.Encoders...)
	c.Auto = map[string]string{}
	for _, codec := range Codecs {
		c.Auto[codec] = c.Best(codec)
	}
	d.mu.Unlock()
	return c
}

// Probe test-encodes a few frames with every hardware encoder that ffmpeg
// has and whose device exists, so "auto" only picks one that really works.
func (d *Detector) Probe(ffmpeg string) {
	d.mu.Lock()
	if d.probing {
		d.mu.Unlock()
		return
	}
	d.probing = true
	if d.caps == nil || d.caps.FFmpeg != ffmpeg {
		d.caps = detectFeatures(ffmpeg)
	}
	encs := append([]Encoder(nil), d.caps.Encoders...)
	nodes := d.caps.RenderNodes
	d.mu.Unlock()

	for i := range encs {
		e := &encs[i]
		if !e.InFFmpeg {
			continue
		}
		e.Tested, e.Works, e.Error = true, false, ""
		if e.Kind == "cpu" {
			e.Works = true
			continue
		}
		args, err := probeArgs(e.Kind, e.Name, nodes)
		if err != nil {
			e.Error = err.Error()
			continue
		}
		out, err := run(20*time.Second, ffmpeg, args...)
		if err != nil {
			e.Error = lastLines(out, 2)
			if e.Error == "" {
				e.Error = err.Error()
			}
			continue
		}
		e.Works = true
	}
	var works []string
	for _, e := range encs {
		if e.Works && e.Kind != "cpu" {
			works = append(works, e.Name)
		}
	}
	if len(works) == 0 {
		log.Printf("transcode: no GPU encoder works here; transcoding uses the CPU")
	} else {
		log.Printf("transcode: working GPU encoders: %s", strings.Join(works, ", "))
	}
	d.mu.Lock()
	if d.caps.FFmpeg == ffmpeg {
		d.caps.Encoders = encs
		d.caps.ProbedAt = time.Now()
	}
	d.probing = false
	d.mu.Unlock()
}

func probeArgs(kind, enc string, nodes []RenderNode) ([]string, error) {
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	src := []string{"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25"}
	tail := []string{"-frames:v", "10", "-c:v", enc, "-f", "null", "-"}
	switch kind {
	case "nvenc":
		if _, err := os.Stat("/dev/nvidia0"); err != nil {
			if _, err := exec.LookPath("nvidia-smi"); err != nil {
				return nil, fmt.Errorf("no NVIDIA GPU visible (/dev/nvidia0 missing)")
			}
		}
		a = append(a, src...)
		a = append(a, "-vf", "format=nv12")
	case "qsv", "vaapi":
		dev := renderNodeFor(kind, nodes, 0)
		if dev == "" {
			return nil, fmt.Errorf("no suitable /dev/dri render device")
		}
		a = append(a, "-init_hw_device", "vaapi=va:"+dev)
		if kind == "qsv" {
			a = append(a, "-init_hw_device", "qsv=qs@va", "-filter_hw_device", "qs")
			a = append(a, src...)
			a = append(a, "-vf", "format=nv12,hwupload=extra_hw_frames=64")
		} else {
			a = append(a, "-filter_hw_device", "va")
			a = append(a, src...)
			a = append(a, "-vf", "format=nv12,hwupload")
		}
	}
	return append(a, tail...), nil
}

// renderNodeFor picks the render node for QSV (Intel only) or VAAPI. index
// selects among suitable nodes (a profile's GPU number).
func renderNodeFor(kind string, nodes []RenderNode, index int) string {
	var ok []string
	for _, n := range nodes {
		switch {
		case kind == "qsv" && n.Vendor == "intel":
			ok = append(ok, n.Path)
		case kind == "vaapi" && (n.Vendor == "intel" || n.Vendor == "amd" || n.Vendor == "other"):
			ok = append(ok, n.Path)
		}
	}
	if len(ok) == 0 {
		return ""
	}
	if index < 0 || index >= len(ok) {
		index = 0
	}
	return ok[index]
}

func detectFeatures(ffmpeg string) *Caps {
	c := &Caps{FFmpeg: ffmpeg, Filters: map[string]bool{}, HWAccels: []string{}}
	if out, err := run(5*time.Second, ffmpeg, "-hide_banner", "-version"); err == nil {
		c.FFmpegOK = true
		c.Version = strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	}
	encList, _ := run(5*time.Second, ffmpeg, "-hide_banner", "-encoders")
	if out, err := run(5*time.Second, ffmpeg, "-hide_banner", "-hwaccels"); err == nil {
		for _, l := range strings.Split(out, "\n")[1:] {
			if l = strings.TrimSpace(l); l != "" {
				c.HWAccels = append(c.HWAccels, l)
			}
		}
	}
	if out, err := run(5*time.Second, ffmpeg, "-hide_banner", "-filters"); err == nil {
		for _, f := range []string{"scale_cuda", "yadif_cuda", "bwdif_cuda", "scale_vaapi", "deinterlace_vaapi", "vpp_qsv", "bwdif", "yadif"} {
			c.Filters[f] = strings.Contains(out, " "+f+" ")
		}
	}
	hasAccel := func(a string) bool {
		for _, h := range c.HWAccels {
			if h == a {
				return true
			}
		}
		return false
	}
	for _, k := range Kinds {
		for _, codec := range Codecs {
			name := EncoderName(k, codec)
			e := Encoder{Kind: k, Codec: codec, Name: name, InFFmpeg: strings.Contains(encList, " "+name+" ")}
			switch k {
			case "nvenc":
				e.HWDecode = hasAccel("cuda")
			case "qsv":
				e.HWDecode = hasAccel("qsv")
			case "vaapi":
				e.HWDecode = hasAccel("vaapi")
			}
			if k == "cpu" && e.InFFmpeg {
				e.Tested, e.Works = true, true
			}
			c.Encoders = append(c.Encoders, e)
		}
	}
	c.RenderNodes = renderNodes()
	return c
}

func renderNodes() []RenderNode {
	paths, _ := filepath.Glob("/dev/dri/renderD*")
	sort.Strings(paths)
	out := []RenderNode{}
	for _, p := range paths {
		n := RenderNode{Path: p, Vendor: "other"}
		base := filepath.Join("/sys/class/drm", filepath.Base(p), "device")
		v, _ := os.ReadFile(filepath.Join(base, "vendor"))
		switch strings.TrimSpace(string(v)) {
		case "0x8086":
			n.Vendor = "intel"
		case "0x1002":
			n.Vendor = "amd"
		case "0x10de":
			n.Vendor = "nvidia"
		}
		if l, err := os.Readlink(filepath.Join(base, "driver")); err == nil {
			n.Driver = filepath.Base(l)
		}
		out = append(out, n)
	}
	return out
}

func queryGPUs() ([]GPU, string) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil, ""
	}
	out, err := run(5*time.Second, "nvidia-smi", "--query-gpu=index,name,utilization.gpu,utilization.encoder,utilization.decoder,memory.used,memory.total,temperature.gpu,encoder.stats.sessionCount",
		"--format=csv,noheader,nounits")
	if err != nil {
		return nil, "nvidia-smi: " + lastLines(out, 1)
	}
	var gpus []GPU
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 9 {
			continue
		}
		num := func(i int) float64 {
			v, _ := strconv.ParseFloat(strings.TrimSpace(f[i]), 64)
			return v
		}
		gpus = append(gpus, GPU{Index: int(num(0)), Name: strings.TrimSpace(f[1]), Util: num(2), EncUtil: num(3), DecUtil: num(4),
			MemUsedMB: num(5), MemTotalMB: num(6), TempC: num(7), Sessions: int(num(8))})
	}
	return gpus, ""
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}
