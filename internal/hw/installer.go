package hw

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The web-triggered installer is a privilege boundary: dvbhub (unprivileged,
// possibly in a container) only writes <data>/driver/requests/<id>.req
// containing one whitelisted action word. The host-side service installed by
// "install-drivers.sh --enable-web-trigger" (root, systemd path unit) runs the
// action and writes jobs/<id>.status and jobs/<id>.log back.

// Actions the host installer accepts.
var Actions = map[string]string{
	"firmware": "Install DVB firmware (distro package + LibreELEC firmware collection; never overwrites existing files)",
	"tbs":      "Build and install the TBS open-source driver package for the running kernel (15-40 min; re-run after kernel updates)",
	"tools":    "Install DVB command-line tools (dvbv5-scan, dvb-fe-tool) for troubleshooting",
}

type InstallerInfo struct {
	Enabled   bool   `json:"enabled"`
	Host      string `json:"host,omitempty"`
	Version   string `json:"version,omitempty"`
	Installed string `json:"installed,omitempty"`
	Command   string `json:"command"` // how to enable / run by hand
	Jobs      []Job  `json:"jobs"`
}

type Job struct {
	ID      string    `json:"id"`
	Action  string    `json:"action"`
	Status  string    `json:"status"` // queued, running, ok, failed
	Created time.Time `json:"created"`
	Log     string    `json:"log,omitempty"`
	Reboot  bool      `json:"rebootRecommended"`
}

func (d *Detector) driverDir(sub string) string { return filepath.Join(d.dataDir, "driver", sub) }

// Installer reports whether the host installer is enabled and lists recent jobs.
func (d *Detector) Installer() InstallerInfo {
	info := InstallerInfo{Command: "curl -fsSL https://raw.githubusercontent.com/Madmaxx636/dvbhub/main/deploy/install-drivers.sh | sudo bash -s -- --enable-web-trigger <dvbhub data dir>"}
	var marker struct {
		Host      string `json:"host"`
		Version   string `json:"version"`
		Installed string `json:"installed"`
	}
	if b, err := os.ReadFile(filepath.Join(d.dataDir, "driver", "enabled")); err == nil {
		info.Enabled = true
		json.Unmarshal(b, &marker)
		info.Host, info.Version, info.Installed = marker.Host, marker.Version, marker.Installed
	}
	reqs, _ := filepath.Glob(filepath.Join(d.driverDir("requests"), "*.req"))
	jobs, _ := filepath.Glob(filepath.Join(d.driverDir("jobs"), "*.status"))
	ids := map[string]bool{}
	for _, p := range append(reqs, jobs...) {
		ids[strings.TrimSuffix(strings.TrimSuffix(filepath.Base(p), ".req"), ".status")] = true
	}
	for id := range ids {
		if j, err := d.Job(id, false); err == nil {
			info.Jobs = append(info.Jobs, j)
		}
	}
	sort.Slice(info.Jobs, func(i, j int) bool { return info.Jobs[i].Created.After(info.Jobs[j].Created) })
	if len(info.Jobs) > 10 {
		info.Jobs = info.Jobs[:10]
	}
	return info
}

// Request queues an installer action for the host service.
func (d *Detector) Request(action string) (Job, error) {
	if _, ok := Actions[action]; !ok {
		return Job{}, fmt.Errorf("unknown action %q", action)
	}
	if !d.Installer().Enabled {
		return Job{}, errors.New("the host installer is not enabled; run install-drivers.sh --enable-web-trigger on the host first")
	}
	for _, j := range d.Installer().Jobs {
		if j.Status == "queued" || j.Status == "running" {
			return Job{}, fmt.Errorf("an installer job (%s) is already %s", j.Action, j.Status)
		}
	}
	id := time.Now().UTC().Format("20060102-150405")
	if err := os.MkdirAll(d.driverDir("requests"), 0o775); err != nil {
		return Job{}, err
	}
	tmp := filepath.Join(d.dataDir, "driver", "."+id+".tmp")
	if err := os.WriteFile(tmp, []byte(action+"\n"), 0o664); err != nil {
		return Job{}, err
	}
	if err := os.Rename(tmp, filepath.Join(d.driverDir("requests"), id+".req")); err != nil {
		return Job{}, err
	}
	return Job{ID: id, Action: action, Status: "queued", Created: time.Now()}, nil
}

var jobID = func(id string) bool {
	if len(id) == 0 || len(id) > 40 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// Job returns a job's state and (optionally) its log.
func (d *Detector) Job(id string, withLog bool) (Job, error) {
	if !jobID(id) {
		return Job{}, errors.New("bad job id")
	}
	j := Job{ID: id}
	if t, err := time.Parse("20060102-150405", id); err == nil {
		j.Created = t
	}
	if b, err := os.ReadFile(filepath.Join(d.driverDir("requests"), id+".req")); err == nil {
		j.Action, j.Status = strings.TrimSpace(string(b)), "queued"
	}
	if b, err := os.ReadFile(filepath.Join(d.driverDir("jobs"), id+".status")); err == nil {
		fields := strings.Fields(string(b)) // "<action> <status> [reboot]"
		if len(fields) >= 2 {
			j.Action, j.Status = fields[0], fields[1]
			j.Reboot = len(fields) > 2 && fields[2] == "reboot"
		}
	}
	if j.Action == "" {
		return Job{}, errors.New("job not found")
	}
	if withLog {
		if b, err := os.ReadFile(filepath.Join(d.driverDir("jobs"), id+".log")); err == nil {
			if len(b) > 64<<10 {
				b = b[len(b)-64<<10:]
			}
			j.Log = string(b)
		}
	}
	return j, nil
}
