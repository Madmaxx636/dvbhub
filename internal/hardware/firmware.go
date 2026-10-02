package hardware

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	// "si2157 9-0060: Direct firmware load for x.fw failed with error -2"
	klogDevice = regexp.MustCompile(`^(?:\[[^\]]*\]\s*)?(\S+ \S+?):\s`)
	fwFailed   = []*regexp.Regexp{
		regexp.MustCompile(`(?i)direct firmware load for (\S+) failed`),
		regexp.MustCompile(`(?i)firmware file '?([\w./+-]+)'? not found`),
		regexp.MustCompile(`(?i)failed to load firmware '?([\w./+-]+\.(?:fw|bin|inp))`),
		regexp.MustCompile(`(?i)request_firmware.*?failed.*?([\w./+-]+\.(?:fw|bin|inp))`),
	}
	fwLoaded = regexp.MustCompile(`(?i)downloading firmware|firmware version|firmware download|loaded firmware|firmware: .* loaded`)
)

// neededFirmware returns the firmware files the kernel log shows a driver
// failing to load, unless the same device loaded some firmware afterwards
// (drivers often try an optional file first, then fall back to another).
func neededFirmware(klog []string) map[string]bool {
	failedBy := map[string][]string{} // device -> files that failed since its last success
	for _, line := range klog {
		dev := ""
		if m := klogDevice.FindStringSubmatch(line); m != nil {
			dev = m[1]
		}
		for _, re := range fwFailed {
			if m := re.FindStringSubmatch(line); m != nil {
				failedBy[dev] = append(failedBy[dev], filepath.Base(strings.Trim(m[1], "'\"")))
				break
			}
		}
		if fwLoaded.MatchString(line) && dev != "" {
			delete(failedBy, dev)
		}
	}
	out := map[string]bool{}
	for _, files := range failedBy {
		for _, f := range files {
			if !firmwarePresent(f) {
				out[f] = true
			}
		}
	}
	return out
}

// dvbModules lists loaded kernel modules related to DVB.
func dvbModules() []string {
	f, err := os.Open("/proc/modules")
	if err != nil {
		return nil
	}
	defer f.Close()
	users := map[string][]string{}
	var all []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		all = append(all, fields[0])
		if fields[3] != "-" {
			for _, u := range strings.Split(strings.TrimSuffix(fields[3], ","), ",") {
				users[fields[0]] = append(users[fields[0]], u)
			}
		}
	}
	set := map[string]bool{}
	var walk func(string)
	walk = func(m string) {
		if set[m] {
			return
		}
		set[m] = true
		for _, u := range users[m] {
			walk(u)
		}
	}
	walk("dvb_core")
	walk("dvb_usb_v2")
	walk("dvb_usb")
	for _, m := range all {
		if dvbModule.MatchString(m) {
			set[m] = true
		}
	}
	var out []string
	for _, m := range all {
		if set[m] {
			out = append(out, m)
		}
	}
	return out
}

func firmwarePresent(file string) bool {
	for _, dir := range []string{"/lib/firmware", "/lib/firmware/updates", "/usr/lib/firmware"} {
		for _, ext := range []string{"", ".xz", ".zst"} {
			if _, err := os.Stat(filepath.Join(dir, file+ext)); err == nil {
				return true
			}
		}
	}
	return false
}

// firmwareList returns needed firmware first, then files that loaded DVB
// drivers can use but that aren't installed (optional: often for other
// chip revisions than the one plugged in).
func firmwareList(needed map[string]bool, r *Report) []Firmware {
	out := []Firmware{}
	seen := map[string]bool{}
	mods := dvbModules()
	if len(mods) > 0 {
		if _, err := exec.LookPath("modinfo"); err != nil {
			r.Notes = append(r.Notes, "modinfo isn't available, so the firmware lists of drivers can't be checked.")
		}
	}
	for _, m := range mods {
		txt, err := run("modinfo", "-F", "firmware", m)
		if err != nil {
			continue
		}
		for _, fw := range strings.Fields(txt) {
			if seen[fw] {
				continue
			}
			seen[fw] = true
			present := firmwarePresent(fw)
			if !present || needed[filepath.Base(fw)] {
				out = append(out, Firmware{Module: m, File: fw, Present: present, Needed: needed[filepath.Base(fw)]})
			}
		}
	}
	for fw := range needed {
		if !seen[fw] {
			out = append(out, Firmware{Module: "(kernel log)", File: fw, Needed: true})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Needed != out[j].Needed {
			return out[i].Needed
		}
		return out[i].File < out[j].File
	})
	return out
}
