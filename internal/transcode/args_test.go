package transcode

import (
	"strings"
	"testing"

	"dvbhub/internal/config"
)

func profile() *config.Profile {
	p := *config.DefaultProfiles()["hd"]
	return &p
}

func has(args []string, seq ...string) bool {
	return strings.Contains(" "+strings.Join(args, " ")+" ", " "+strings.Join(seq, " ")+" ")
}

func TestCPUConstantBitrate(t *testing.T) {
	p := profile()
	p.Encoder, p.RateControl, p.BitrateK, p.Height = "cpu", "cbr", 3000, 480
	a := Args(p, Plan{Kind: "cpu", Encoder: "libx264"}, "MPEG2VIDEO", map[string]bool{"bwdif": true})
	for _, want := range [][]string{{"-c:v", "libx264"}, {"-b:v", "3000k"}, {"-maxrate", "3000k"}, {"-x264-params", "nal-hrd=cbr"}, {"-f", "mpegts"}} {
		if !has(a, want...) {
			t.Errorf("missing %v in %v", want, a)
		}
	}
	vf := a[indexOf(a, "-vf")+1]
	if !strings.Contains(vf, "bwdif=") || !strings.Contains(vf, "scale=-2:480") {
		t.Errorf("filters %q", vf)
	}
}

func TestNVENCWithGPUDecode(t *testing.T) {
	p := profile()
	a := Args(p, Plan{Kind: "nvenc", Encoder: "h264_nvenc", HWDecode: true}, "HEVC", map[string]bool{"bwdif_cuda": true})
	if !has(a, "-hwaccel", "cuda") || !has(a, "-c:v", "h264_nvenc") || !has(a, "-rc", "vbr") {
		t.Errorf("args %v", a)
	}
	vf := a[indexOf(a, "-vf")+1]
	if !strings.Contains(vf, "bwdif_cuda") || !strings.Contains(vf, "scale_cuda=w=-2:h=1080:format=nv12") {
		t.Errorf("filters %q", vf)
	}
}

func TestQSVNeedsAKnownDecoder(t *testing.T) {
	p := profile()
	a := Args(p, Plan{Kind: "qsv", Encoder: "h264_qsv", HWDecode: true, Device: "/dev/dri/renderD128"}, "MPEG2VIDEO", nil)
	if !has(a, "-c:v", "mpeg2_qsv") || !strings.Contains(a[indexOf(a, "-vf")+1], "vpp_qsv") {
		t.Errorf("args %v", a)
	}
	// Unknown source codec: decode on the CPU and upload frames.
	a = Args(p, Plan{Kind: "qsv", Encoder: "h264_qsv", HWDecode: true, Device: "/dev/dri/renderD128"}, "", nil)
	if has(a, "-hwaccel", "qsv") || !strings.Contains(a[indexOf(a, "-vf")+1], "hwupload") {
		t.Errorf("args %v", a)
	}
}

func TestPlansFallBackToCPU(t *testing.T) {
	caps := &Caps{Encoders: []Encoder{{Kind: "vaapi", Codec: "h264", Works: true, InFFmpeg: true, HWDecode: true}},
		RenderNodes: []RenderNode{{Path: "/dev/dri/renderD128", Vendor: "intel"}}}
	plans := Plans(profile(), "auto", caps)
	if len(plans) != 3 || plans[0].Kind != "vaapi" || !plans[0].HWDecode || plans[1].HWDecode || plans[2].Kind != "cpu" {
		t.Fatalf("plans %+v", plans)
	}
	if got := Plans(profile(), "auto", &Caps{}); len(got) != 1 || got[0].Encoder != "libx264" {
		t.Fatalf("no GPU: %+v", got)
	}
}

func TestMPEG2Mode(t *testing.T) {
	p := config.DefaultProfiles()["mpeg2"]
	if !NeedsFFmpeg(p, "MPEG2VIDEO") || NeedsFFmpeg(p, "H264") {
		t.Fatal("mpeg2 mode must convert only MPEG-2 video")
	}
	if NeedsFFmpeg(config.DefaultProfiles()["original"], "MPEG2VIDEO") {
		t.Fatal("passthrough must never run ffmpeg")
	}
}

func indexOf(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return len(a) - 2
}
