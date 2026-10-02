package transcode

import (
	"fmt"
	"strconv"
	"strings"

	"dvbhub/internal/config"
)

// Plan is one way of running a profile: which encoder kind, whether the GPU
// also decodes, and which device.
type Plan struct {
	Kind     string `json:"kind"` // nvenc, qsv, vaapi, cpu
	Encoder  string `json:"encoder"`
	HWDecode bool   `json:"hwDecode"`
	Device   string `json:"device,omitempty"` // render node for qsv/vaapi
	GPU      int    `json:"gpu"`              // CUDA device for nvenc
}

// Plans lists the attempts for a profile, best first: the chosen GPU with
// GPU decoding, the same GPU with CPU decoding, then the CPU encoder.
func Plans(p *config.Profile, defaultKind string, caps *Caps) []Plan {
	codec := p.Codec
	if codec == "" {
		codec = "h264"
	}
	kind := p.Encoder
	if kind == "" || kind == "auto" {
		kind = defaultKind
	}
	if kind == "" || kind == "auto" {
		kind = caps.Best(codec)
	}
	var out []Plan
	if kind != "cpu" {
		dev := ""
		if kind == "qsv" || kind == "vaapi" {
			dev = renderNodeFor(kind, caps.RenderNodes, p.GPU)
			if dev == "" {
				dev = "/dev/dri/renderD128"
			}
		}
		base := Plan{Kind: kind, Encoder: EncoderName(kind, codec), Device: dev, GPU: p.GPU}
		if caps.Encoder(kind, codec).HWDecode {
			hw := base
			hw.HWDecode = true
			out = append(out, hw)
		}
		out = append(out, base)
	}
	return append(out, Plan{Kind: "cpu", Encoder: EncoderName("cpu", codec)})
}

func kb(v int) string { return strconv.Itoa(v) + "k" }

var presets = map[string]map[string]string{
	"nvenc":     {"fast": "p2", "balanced": "p4", "quality": "p6"},
	"qsv":       {"fast": "veryfast", "balanced": "medium", "quality": "slow"},
	"libx264":   {"fast": "ultrafast", "balanced": "veryfast", "quality": "fast"},
	"libx265":   {"fast": "ultrafast", "balanced": "superfast", "quality": "faster"},
	"libsvtav1": {"fast": "12", "balanced": "10", "quality": "8"},
}

// Args returns ffmpeg's arguments (without the program name) for a profile
// run with plan. sourceCodec is the service's video codec (e.g. HEVC), used
// to keep GPU frames in a format the encoder accepts.
func Args(p *config.Profile, plan Plan, sourceCodec string, filters map[string]bool) []string {
	a := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-progress", "pipe:3", "-stats_period", "2"}
	switch plan.Kind {
	case "nvenc":
		a = append(a, "-init_hw_device", "cuda=cu:"+strconv.Itoa(plan.GPU), "-filter_hw_device", "cu")
		if plan.HWDecode {
			a = append(a, "-hwaccel", "cuda", "-hwaccel_output_format", "cuda", "-hwaccel_device", "cu")
		}
	case "qsv":
		a = append(a, "-init_hw_device", "vaapi=va:"+plan.Device, "-init_hw_device", "qsv=qs@va", "-filter_hw_device", "qs")
		if dec := qsvDecoder(sourceCodec); plan.HWDecode && dec != "" {
			a = append(a, "-hwaccel", "qsv", "-hwaccel_output_format", "qsv", "-hwaccel_device", "qs", "-c:v", dec)
		}
	case "vaapi":
		a = append(a, "-init_hw_device", "vaapi=va:"+plan.Device, "-filter_hw_device", "va")
		if plan.HWDecode {
			a = append(a, "-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi", "-hwaccel_device", "va")
		}
	}
	a = append(a, "-fflags", "+genpts+discardcorrupt", "-err_detect", "ignore_err",
		"-analyzeduration", "2000000", "-probesize", "4000000", "-thread_queue_size", "1024",
		"-f", "mpegts", "-i", "pipe:0")

	a = append(a, "-map", "0:v:0?")
	if p.AudioTracks == "first" {
		a = append(a, "-map", "0:a:0?")
	} else {
		a = append(a, "-map", "0:a?")
	}
	if p.Subtitles == "keep" {
		a = append(a, "-map", "0:s?", "-c:s", "copy")
	} else {
		a = append(a, "-sn")
	}
	a = append(a, "-dn", "-ignore_unknown")

	if vf := videoFilters(p, plan, sourceCodec, filters); vf != "" {
		a = append(a, "-vf", vf)
	}
	if p.FPS > 0 {
		a = append(a, "-r", strconv.Itoa(p.FPS))
	}
	a = append(a, "-c:v", plan.Encoder)
	a = append(a, rateArgs(p, plan)...)
	gop := p.GOP
	if gop <= 0 {
		gop = 60
		if p.FPS > 0 {
			gop = 2 * p.FPS
		}
	}
	a = append(a, "-g", strconv.Itoa(gop))
	if plan.Kind == "nvenc" {
		a = append(a, "-forced-idr", "1")
	}

	switch p.AudioCodec {
	case "", "copy":
		a = append(a, "-c:a", "copy")
	default:
		enc := map[string]string{"aac": "aac", "ac3": "ac3", "opus": "libopus"}[p.AudioCodec]
		if enc == "" {
			enc = "aac"
		}
		a = append(a, "-c:a", enc, "-ar", "48000")
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
	return append(a, "-max_muxing_queue_size", "4096", "-f", "mpegts", "-mpegts_flags", "+resend_headers",
		"-muxdelay", "0", "-muxpreload", "0", "pipe:1")
}

// qsvDecoder is the QSV decoder for a source codec ("" if unknown).
func qsvDecoder(sourceCodec string) string {
	return map[string]string{"MPEG2VIDEO": "mpeg2_qsv", "H264": "h264_qsv", "HEVC": "hevc_qsv"}[sourceCodec]
}

func deinterlaceMode(p *config.Profile) string {
	switch p.Deinterlace {
	case "off":
		return ""
	case "on":
		return "all"
	}
	return "interlaced" // auto: only frames flagged as interlaced
}

func videoFilters(p *config.Profile, plan Plan, sourceCodec string, filters map[string]bool) string {
	var f []string
	deint := deinterlaceMode(p)
	cpuFilters := func() {
		if deint != "" {
			if filters["bwdif"] || len(filters) == 0 {
				f = append(f, "bwdif=mode=send_frame:parity=auto:deint="+deint)
			} else {
				f = append(f, "yadif=mode=send_frame:parity=auto:deint="+deint)
			}
		}
		if p.Height > 0 {
			f = append(f, fmt.Sprintf("scale=-2:%d", p.Height))
		}
	}
	switch {
	case plan.Kind == "nvenc" && plan.HWDecode:
		if deint != "" {
			if filters["bwdif_cuda"] {
				f = append(f, "bwdif_cuda=mode=send_frame:parity=auto:deint="+deint)
			} else {
				f = append(f, "yadif_cuda=mode=send_frame:parity=auto:deint="+deint)
			}
		}
		var sc []string
		if p.Height > 0 {
			sc = append(sc, "w=-2", fmt.Sprintf("h=%d", p.Height))
		}
		if sourceCodec == "HEVC" { // may be 10-bit; NVENC H.264 needs 8-bit
			sc = append(sc, "format=nv12")
		}
		if len(sc) > 0 {
			f = append(f, "scale_cuda="+strings.Join(sc, ":"))
		}
	case plan.Kind == "qsv" && plan.HWDecode && qsvDecoder(sourceCodec) != "":
		opts := []string{}
		if deint != "" {
			opts = append(opts, "deinterlace=2")
		}
		if p.Height > 0 {
			opts = append(opts, "w=-1", fmt.Sprintf("h=%d", p.Height))
		}
		if len(opts) > 0 {
			f = append(f, "vpp_qsv="+strings.Join(opts, ":"))
		}
	case plan.Kind == "vaapi" && plan.HWDecode:
		if deint != "" {
			auto := "1"
			if deint == "all" {
				auto = "0"
			}
			f = append(f, "deinterlace_vaapi=rate=frame:auto="+auto)
		}
		if p.Height > 0 {
			f = append(f, fmt.Sprintf("scale_vaapi=w=-2:h=%d:format=nv12", p.Height))
		} else {
			f = append(f, "scale_vaapi=format=nv12")
		}
	default:
		cpuFilters()
		switch plan.Kind {
		case "qsv":
			f = append(f, "format=nv12", "hwupload=extra_hw_frames=64")
		case "vaapi":
			f = append(f, "format=nv12", "hwupload")
		case "nvenc":
			f = append(f, "format=nv12")
		default:
			f = append(f, "format=yuv420p")
		}
	}
	return strings.Join(f, ",")
}

func rateArgs(p *config.Profile, plan Plan) []string {
	b, m := p.BitrateK, p.MaxrateK
	if b <= 0 {
		b = 4000
	}
	if m < b {
		m = b * 3 / 2
	}
	buf := p.BufsizeK
	if buf <= 0 {
		buf = 2 * b
	}
	q := p.Quality
	if q <= 0 {
		q = 23
	}
	preset := func() []string {
		key := plan.Kind
		if plan.Kind == "cpu" {
			key = plan.Encoder
		}
		if v := presets[key][p.Preset]; v != "" {
			return []string{"-preset", v}
		}
		if v := presets[key]["balanced"]; v != "" {
			return []string{"-preset", v}
		}
		return nil
	}
	var a []string
	switch plan.Kind {
	case "nvenc":
		a = append(a, preset()...)
		switch p.RateControl {
		case "cbr":
			a = append(a, "-rc", "cbr", "-b:v", kb(b), "-maxrate", kb(b), "-bufsize", kb(buf))
		case "cq":
			a = append(a, "-rc", "vbr", "-cq", strconv.Itoa(q), "-b:v", "0")
			if p.MaxrateK > 0 {
				a = append(a, "-maxrate", kb(p.MaxrateK), "-bufsize", kb(buf))
			}
		default:
			a = append(a, "-rc", "vbr", "-b:v", kb(b), "-maxrate", kb(m), "-bufsize", kb(buf))
		}
	case "qsv":
		a = append(a, preset()...)
		switch p.RateControl {
		case "cbr":
			a = append(a, "-b:v", kb(b), "-maxrate", kb(b), "-bufsize", kb(buf))
		case "cq":
			a = append(a, "-global_quality", strconv.Itoa(q))
		default:
			a = append(a, "-b:v", kb(b), "-maxrate", kb(m), "-bufsize", kb(buf))
		}
	case "vaapi":
		switch p.RateControl {
		case "cbr":
			a = append(a, "-rc_mode", "CBR", "-b:v", kb(b), "-maxrate", kb(b), "-bufsize", kb(buf))
		case "cq":
			a = append(a, "-rc_mode", "CQP", "-qp", strconv.Itoa(q))
		default:
			a = append(a, "-rc_mode", "VBR", "-b:v", kb(b), "-maxrate", kb(m), "-bufsize", kb(buf))
		}
	default: // CPU: libx264, libx265, libsvtav1
		a = append(a, preset()...)
		switch {
		case p.RateControl == "cq":
			a = append(a, "-crf", strconv.Itoa(q))
			if p.MaxrateK > 0 {
				a = append(a, "-maxrate", kb(p.MaxrateK), "-bufsize", kb(buf))
			}
		case p.RateControl == "cbr" && plan.Encoder == "libx264":
			a = append(a, "-b:v", kb(b), "-minrate", kb(b), "-maxrate", kb(b), "-bufsize", kb(buf), "-x264-params", "nal-hrd=cbr")
		case p.RateControl == "cbr":
			a = append(a, "-b:v", kb(b), "-maxrate", kb(b), "-bufsize", kb(buf))
		case plan.Encoder == "libsvtav1":
			a = append(a, "-b:v", kb(b))
		default:
			a = append(a, "-b:v", kb(b), "-maxrate", kb(m), "-bufsize", kb(buf))
		}
	}
	return a
}

// CommandLine shows the command a profile would run (for the UI).
func CommandLine(ffmpeg string, p *config.Profile, plan Plan, filters map[string]bool) string {
	return ffmpeg + " " + strings.Join(Args(p, plan, "", filters), " ")
}
