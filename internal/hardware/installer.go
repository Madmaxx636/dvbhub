package hardware

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

// The installer is a privilege boundary. dvbhub runs unprivileged (often in
// a container) and only writes <data>/driver/requests/<id>.req holding one
// word from Actions. The host side, installed on purpose by the owner with
// "install-drivers.sh --enable-web-trigger" (a root systemd path unit),
// checks that word against the same fixed list, runs it and writes
// jobs/<id>.status and jobs/<id>.log back. Nothing else is ever executed.

// Action is an installer action the host side accepts.
type Action struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Actions the host installer accepts, in the order the UI lists them.
var Actions = []Action{
	{"firmware", "Install firmware", "Adds missing tuner firmware (your distribution's package plus the LibreELEC DVB firmware collection). Existing files are never replaced."},
	{"replug", "Reset USB tuners", "Resets USB tuners as if they were unplugged and plugged back in, so drivers load newly installed firmware."},
	{"tools", "Install DVB tools", "Installs dvbv5-scan, dvb-fe-tool and similar command-line tools for troubleshooting."},
	{"tbs", "Build TBS drivers", "Builds and installs TBS's open-source drivers for the running kernel (15-40 minutes; needs Secure Boot off; repeat after kernel updates)."},
}

func validAction(a string) bool {
	for _, x := range Actions {
		if x.ID == a {
			return true
		}
	}
	return false
}

// InstallerInfo says whether the host installer is enabled and lists recent jobs.
type InstallerInfo struct {
	Enabled   bool     `json:"enabled"`
	Host      string   `json:"host,omitempty"`
	Version   string   `json:"version,omitempty"`
	Installed string   `json:"installed,omitempty"`
	Actions   []Action `json:"actions"`
	Jobs      []Job    `json:"jobs"`
}

type Job struct {
	ID      string    `json:"id"`
	Action  string    `json:"action"`
	Status  string    `json:"status"` // queued, running, ok, failed
	Created time.Time `json:"created"`
	Log     string    `json:"log,omitempty"`
	Reboot  bool      `json:"replugRecommended"`
}

func (d *Detector) driverDir(sub string) string { return filepath.Join(d.dataDir, "driver", sub) }

// Installer reports the installer state and the last 10 jobs.
func (d *Detector) Installer() InstallerInfo {
	info := InstallerInfo{Actions: Actions, Jobs: []Job{}}
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
	sort.Slice(info.Jobs, func(i, j int) bool { return info.Jobs[i].ID > info.Jobs[j].ID })
	if len(info.Jobs) > 10 {
		info.Jobs = info.Jobs[:10]
	}
	return info
}

// Request queues an installer action for the host side.
func (d *Detector) Request(action string) (Job, error) {
	if !validAction(action) {
		return Job{}, fmt.Errorf("unknown action %q", action)
	}
	info := d.Installer()
	if !info.Enabled {
		return Job{}, errors.New("the host installer isn't set up yet; run the one-time setup command on the host first")
	}
	for _, j := range info.Jobs {
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

func validJobID(id string) bool {
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

// Job returns a job's state and, optionally, its log.
func (d *Detector) Job(id string, withLog bool) (Job, error) {
	if !validJobID(id) {
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
