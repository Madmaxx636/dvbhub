package stream

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GPU is one NVIDIA GPU as reported by nvidia-smi.
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

// Caps describes transcoding capabilities of this host.
type Caps struct {
	FFmpeg     string   `json:"ffmpeg"`
	FFmpegOK   bool     `json:"ffmpegOk"`
	Version    string   `json:"version"`
	Encoders   []string `json:"encoders"` // interesting encoders available
	CUDA       bool     `json:"cuda"`     // hwaccel cuda available
	CUDAFilter bool     `json:"cudaFilters"`
	GPUs       []GPU    `json:"gpus"`
	SMIError   string   `json:"smiError,omitempty"`
}

var (
	capsMu    sync.Mutex
	capsCache *Caps
	capsFor   string
	gpuAt     time.Time
)

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// Detect probes ffmpeg once and nvidia-smi at most every 2 seconds.
func Detect(ffmpeg string) Caps {
	capsMu.Lock()
	defer capsMu.Unlock()
	if capsCache == nil || capsFor != ffmpeg {
		c := &Caps{FFmpeg: ffmpeg}
		if out, err := run(ffmpeg, "-hide_banner", "-version"); err == nil {
			c.FFmpegOK = true
			c.Version = strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
		}
		if out, err := run(ffmpeg, "-hide_banner", "-encoders"); err == nil {
			for _, e := range []string{"h264_nvenc", "hevc_nvenc", "av1_nvenc", "libx264", "libx265", "aac", "ac3"} {
				if strings.Contains(out, " "+e+" ") {
					c.Encoders = append(c.Encoders, e)
				}
			}
		}
		if out, err := run(ffmpeg, "-hide_banner", "-hwaccels"); err == nil {
			c.CUDA = strings.Contains(out, "cuda")
		}
		if out, err := run(ffmpeg, "-hide_banner", "-filters"); err == nil {
			c.CUDAFilter = strings.Contains(out, "scale_cuda") && strings.Contains(out, "yadif_cuda")
		}
		capsCache, capsFor = c, ffmpeg
	}
	if time.Since(gpuAt) > 2*time.Second {
		gpuAt = time.Now()
		capsCache.GPUs, capsCache.SMIError = queryGPUs()
	}
	c := *capsCache
	return c
}

func queryGPUs() ([]GPU, string) {
	out, err := run("nvidia-smi", "--query-gpu=index,name,utilization.gpu,utilization.encoder,utilization.decoder,memory.used,memory.total,temperature.gpu,encoder.stats.sessionCount",
		"--format=csv,noheader,nounits")
	if err != nil {
		return nil, "nvidia-smi: " + err.Error()
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
