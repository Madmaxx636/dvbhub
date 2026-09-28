// Package hw detects DVB tuner hardware, its drivers and missing firmware,
// and hands driver/firmware installation requests to a host-side installer.
package hw

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Device is a USB or PCI device that looks like a TV tuner.
type Device struct {
	Bus      string   `json:"bus"` // usb, pci
	ID       string   `json:"id"`  // vendor:product
	Name     string   `json:"name"`
	Brand    string   `json:"brand"`
	Driver   string   `json:"driver,omitempty"`
	Adapters []string `json:"adapters,omitempty"` // e.g. adapter0
	Status   string   `json:"status"`             // working, no-adapter, no-driver
	Hints    []string `json:"hints,omitempty"`
}

// Firmware is a firmware file a loaded DVB module can request.
type Firmware struct {
	Module   string `json:"module"`
	File     string `json:"file"`
	Present  bool   `json:"present"`
	Required bool   `json:"required"` // the kernel log shows it failed to load
}

type Report struct {
	Kernel      string     `json:"kernel"`
	Container   bool       `json:"container"`
	SecureBoot  string     `json:"secureBoot"` // enabled, disabled, unknown
	Adapters    []string   `json:"adapters"`
	Devices     []Device   `json:"devices"`
	Firmware    []Firmware `json:"firmware"` // missing only
	Blacklisted []string   `json:"blacklisted,omitempty"`
	KernelLog   []string   `json:"kernelLog,omitempty"`
	Notes       []string   `json:"notes,omitempty"`
	Summary     string     `json:"summary"`
	Actions     []string   `json:"actions"` // suggested installer actions: firmware, tbs, tools
}

type brand struct {
	name string
	hint string
	tbs  bool
}

// USB vendor ids of TV tuner makers (0bda Realtek is matched by product below).
var usbVendors = map[string]brand{
	"2040": {"Hauppauge", "Hauppauge devices usually just need firmware files (installer: firmware).", false},
	"734c": {"TBS", "TBS devices often need the TBS driver package (installer: TBS drivers).", true},
	"0572": {"DVBSky / Conexant", "DVBSky devices work with in-kernel drivers plus firmware.", false},
	"1f4d": {"Geniatech / MyGica", "", false},
	"0ccd": {"TerraTec", "", false},
	"2013": {"PCTV Systems", "PCTV devices usually need firmware files.", false},
	"2304": {"Pinnacle", "", false},
	"048d": {"ITE (IT913x/IT9135)", "IT913x sticks need dvb-usb-it9135 firmware.", false},
	"15f4": {"HanfTek", "", false},
	"1b80": {"Afatech", "", false},
	"07ca": {"AVerMedia", "", false},
	"185b": {"Compro", "", false},
	"0413": {"Leadtek", "", false},
	"0fe9": {"DViCO", "", false},
	"187f": {"Siano", "Siano devices need sms1xxx firmware.", false},
	"1d19": {"Dexatek", "", false},
	"13d3": {"AzureWave", "", false},
	"1554": {"Prolink / PixelView", "", false},
	"eb1a": {"Empia (em28xx)", "", false},
	"1164": {"YUAN", "", false},
}

var realtekDVB = map[string]bool{"2832": true, "2838": true}

// PCI vendor or subsystem-vendor ids of TV tuner makers.
var pciVendors = map[string]brand{
	"544d": {"TBS", "TBS cards often need the TBS driver package (installer: TBS drivers).", true},
	"0070": {"Hauppauge", "Hauppauge cards usually just need firmware files.", false},
	"4254": {"DVBSky", "DVBSky cards work with in-kernel drivers plus firmware.", false},
	"dd01": {"Digital Devices", "Digital Devices cards use the in-kernel ddbridge driver.", false},
	"14f1": {"Conexant bridge", "", false},
	"1131": {"Philips/NXP SAA716x/SAA7164", "", false},
	"18ac": {"DViCO", "", false},
	"1461": {"AVerMedia", "", false},
}

var dvbDriver = regexp.MustCompile(`(?i)dvb|em28xx|cx23885|cx231xx|cx88|saa716|saa7164|smipcie|ddbridge|tbsecp3|tbs|au0828|dw2102|rtl28xxu|af9035|af9015|it913x|mantis|netup|ngene|budget|pctv|smsusb|dm1105|b2c2|pt3|earth-pt|cx18|ivtv|hdpvr|cx25821|saa7134`)
var dvbModule = regexp.MustCompile(`(?i)^(dvb|si21[0-9]{2}|si2165|m88ds|m88rs|m88ts|mxl[0-9]|tda1[0-9]|tda8[0-9]|stv0[0-9]|stv6|stv09|stb0|stb6|cx2[0-9]{4}|em28xx|lgdt|af90|it913|rtl28|mn88|cxd28|ts2020|tuner_|smipcie|ddbridge|tbs|saa716|au08|dw2102|a8293|lnbh|isl64|zl10|mt20|mt2[0-9]{3}|r820|fc00|e4000|dib[0-9]|drxk|drxd|drx39|tua9|or51|s5h|nxt200|mb86a|atbm|avl6|ascot|horus|helene|smsusb|smsdvb|mantis|ngene|budget|b2c2)`)

// Detector caches hardware reports and manages installer jobs.
type Detector struct {
	dataDir string

	mu     sync.Mutex
	report *Report
	at     time.Time
}

func NewDetector(dataDir string) *Detector { return &Detector{dataDir: dataDir} }

func readTrim(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

// Report returns a (cached, 10 s) hardware report. force skips the cache.
func (d *Detector) Report(force bool) Report {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.report == nil || force || time.Since(d.at) > 10*time.Second {
		r := detect()
		d.report, d.at = &r, time.Now()
	}
	return *d.report
}

func detect() Report {
	r := Report{Kernel: readTrim("/proc/sys/kernel/osrelease"), SecureBoot: secureBoot()}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		r.Container = true
		r.Notes = append(r.Notes, "dvbhub runs in a container: drivers and firmware must be installed on the host (the installer runs on the host).")
	}
	adapterDev := map[string][]string{} // resolved sysfs device path -> adapters
	fes, _ := filepath.Glob("/sys/class/dvb/dvb*.frontend*")
	seenAdapter := map[string]bool{}
	for _, fe := range fes {
		base := filepath.Base(fe) // dvb0.frontend0
		var a, f int
		if _, err := fmt.Sscanf(base, "dvb%d.frontend%d", &a, &f); err != nil {
			continue
		}
		name := fmt.Sprintf("adapter%d", a)
		if !seenAdapter[name] {
			seenAdapter[name] = true
			r.Adapters = append(r.Adapters, name)
		}
		if dev, err := filepath.EvalSymlinks(filepath.Join(fe, "device")); err == nil {
			adapterDev[dev] = appendUnique(adapterDev[dev], name)
		}
	}
	sort.Strings(r.Adapters)

	r.Devices = append(r.Devices, usbDevices(adapterDev)...)
	r.Devices = append(r.Devices, pciDevices(adapterDev)...)

	klog := kernelLog()
	failedFW := map[string]bool{}
	fwRe := regexp.MustCompile(`(?i)(?:direct firmware load for|firmware file|failed to load firmware|request_firmware.*?)\s*'?([\w./+-]+\.(?:fw|bin|inp))`)
	for _, line := range klog {
		if m := fwRe.FindStringSubmatch(line); m != nil {
			failedFW[filepath.Base(m[1])] = true
		}
	}
	for _, line := range klog {
		l := strings.ToLower(line)
		dvbish := strings.Contains(l, "dvb") || strings.Contains(l, "frontend") || strings.Contains(l, "demod") ||
			strings.Contains(l, "tuner") || dvbDriver.MatchString(l)
		if dvbish || (strings.Contains(l, "firmware") && fwRe.MatchString(line)) {
			r.KernelLog = append(r.KernelLog, line)
		}
	}
	if len(r.KernelLog) > 40 {
		r.KernelLog = r.KernelLog[len(r.KernelLog)-40:]
	}
	if klog == nil {
		r.Notes = append(r.Notes, "The kernel log is not readable from here, so firmware load errors can't be confirmed.")
	}

	r.Firmware = missingFirmware(failedFW, &r)
	r.Blacklisted = blacklisted()

	// Summary and suggested actions.
	needTBS := false
	for i := range r.Devices {
		dev := &r.Devices[i]
		if b, ok := usbVendors[strings.Split(dev.ID, ":")[0]]; ok && b.tbs && dev.Status != "working" {
			needTBS = true
		}
		if dev.Brand == "TBS" && dev.Status != "working" {
			needTBS = true
		}
	}
	missing := 0
	for _, f := range r.Firmware {
		if !f.Present {
			missing++
		}
	}
	notWorking := 0
	for _, dev := range r.Devices {
		if dev.Status != "working" {
			notWorking++
		}
	}
	switch {
	case len(r.Devices) == 0 && len(r.Adapters) == 0:
		r.Summary = "No TV tuner hardware detected. Check that the tuner is plugged in (USB) or seated (PCIe)."
	case notWorking == 0 && len(r.Adapters) > 0:
		r.Summary = fmt.Sprintf("All good: %d DVB adapter(s) available.", len(r.Adapters))
	default:
		r.Summary = fmt.Sprintf("%d tuner device(s) found, %d not working yet.", len(r.Devices), notWorking)
	}
	if missing > 0 || notWorking > 0 {
		r.Actions = append(r.Actions, "firmware")
	}
	if needTBS {
		r.Actions = append(r.Actions, "tbs")
	}
	if len(r.Blacklisted) > 0 {
		r.Notes = append(r.Notes, "Some DVB driver modules are blacklisted (often by rtl-sdr packages). Remove those lines if you want to use them as TV tuners.")
	}
	if needTBS && r.SecureBoot == "enabled" {
		r.Notes = append(r.Notes, "Secure Boot is enabled: self-built TBS driver modules will not load unless you sign them (MOK) or disable Secure Boot.")
	}
	return r
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

func adaptersUnder(devPath string, adapterDev map[string][]string) []string {
	var out []string
	for p, ads := range adapterDev {
		if p == devPath || strings.HasPrefix(p, devPath+"/") {
			for _, a := range ads {
				out = appendUnique(out, a)
			}
		}
	}
	sort.Strings(out)
	return out
}

func finishDevice(dev *Device, path string, adapterDev map[string][]string) {
	dev.Adapters = adaptersUnder(path, adapterDev)
	switch {
	case len(dev.Adapters) > 0:
		dev.Status = "working"
	case dev.Driver != "":
		dev.Status = "no-adapter"
		dev.Hints = append(dev.Hints, "A driver ("+dev.Driver+") is loaded but no DVB adapter was created: usually missing firmware (check the kernel log) or an unsupported tuner revision.")
	default:
		dev.Status = "no-driver"
		dev.Hints = append(dev.Hints, "No driver is bound to this device. It may need a newer kernel, extra drivers (e.g. TBS) or a module that is blacklisted.")
	}
}

func usbDevices(adapterDev map[string][]string) []Device {
	var out []Device
	devs, _ := filepath.Glob("/sys/bus/usb/devices/*")
	for _, p := range devs {
		vid := readTrim(filepath.Join(p, "idVendor"))
		pid := readTrim(filepath.Join(p, "idProduct"))
		if vid == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		var drivers []string
		ifaces, _ := filepath.Glob(p + ":*")
		for _, ifc := range ifaces {
			if drv, err := os.Readlink(filepath.Join(ifc, "driver")); err == nil {
				drivers = appendUnique(drivers, filepath.Base(drv))
			}
		}
		driver := strings.Join(drivers, ",")
		b, known := usbVendors[vid]
		if vid == "0bda" && realtekDVB[pid] {
			b, known = brand{"Realtek RTL2832U", "RTL2832U sticks use dvb_usb_rtl28xxu. rtl-sdr packages blacklist it; remove that blacklist to use it for TV.", false}, true
		}
		if !known && !dvbDriver.MatchString(driver) {
			continue
		}
		name := strings.TrimSpace(readTrim(filepath.Join(p, "manufacturer")) + " " + readTrim(filepath.Join(p, "product")))
		if name == "" {
			name = "USB device"
		}
		dev := Device{Bus: "usb", ID: vid + ":" + pid, Name: name, Brand: b.name, Driver: driver}
		if dev.Brand == "" {
			dev.Brand = "Unknown"
		}
		if b.hint != "" {
			dev.Hints = append(dev.Hints, b.hint)
		}
		finishDevice(&dev, real, adapterDev)
		out = append(out, dev)
	}
	return out
}

func pciDevices(adapterDev map[string][]string) []Device {
	var out []Device
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	names := pciNames()
	for _, p := range devs {
		vid := strings.TrimPrefix(readTrim(filepath.Join(p, "vendor")), "0x")
		did := strings.TrimPrefix(readTrim(filepath.Join(p, "device")), "0x")
		svid := strings.TrimPrefix(readTrim(filepath.Join(p, "subsystem_vendor")), "0x")
		class := strings.TrimPrefix(readTrim(filepath.Join(p, "class")), "0x")
		driver := ""
		if drv, err := os.Readlink(filepath.Join(p, "driver")); err == nil {
			driver = filepath.Base(drv)
		}
		b, known := pciVendors[svid]
		if !known || b.name == "" {
			b, known = pciVendors[vid]
		}
		multimedia := strings.HasPrefix(class, "0400") || strings.HasPrefix(class, "0480")
		if !(known && b.name != "" && (multimedia || b.tbs)) && !dvbDriver.MatchString(driver) {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		name := names[vid+":"+did]
		if name == "" {
			name = "PCI multimedia device"
		}
		dev := Device{Bus: "pci", ID: vid + ":" + did, Name: name, Brand: b.name, Driver: driver}
		if dev.Brand == "" {
			dev.Brand = "Unknown"
		}
		if b.hint != "" {
			dev.Hints = append(dev.Hints, b.hint)
		}
		finishDevice(&dev, real, adapterDev)
		out = append(out, dev)
	}
	return out
}

// pciNames reads vendor/device names from pci.ids if installed.
func pciNames() map[string]string {
	out := map[string]string{}
	for _, path := range []string{"/usr/share/misc/pci.ids", "/usr/share/hwdata/pci.ids", "/usr/share/pci.ids"} {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		vendor, vname := "", ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "" || line[0] == '#':
			case strings.HasPrefix(line, "C "):
				return out // device classes section follows the vendor list
			case line[0] != '\t' && len(line) > 6:
				vendor, vname = line[:4], strings.TrimSpace(line[4:])
			case strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "\t\t") && len(line) > 6 && vendor != "":
				out[vendor+":"+line[1:5]] = vname + " " + strings.TrimSpace(line[5:])
			}
		}
		return out
	}
	return out
}

func secureBoot() string {
	matches, _ := filepath.Glob("/sys/firmware/efi/efivars/SecureBoot-*")
	if len(matches) == 0 {
		if _, err := os.Stat("/sys/firmware/efi"); err != nil {
			return "disabled" // legacy BIOS boot
		}
		return "unknown"
	}
	b, err := os.ReadFile(matches[0])
	if err != nil || len(b) < 5 {
		return "unknown"
	}
	if b[4] == 1 {
		return "enabled"
	}
	return "disabled"
}

func run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

// kernelLog returns kernel messages, or nil if they cannot be read.
func kernelLog() []string {
	out, err := run("dmesg")
	if err != nil {
		out, err = run("journalctl", "-k", "-b", "--no-pager", "-q", "-n", "3000")
		if err != nil {
			return nil
		}
	}
	return strings.Split(strings.TrimSpace(out), "\n")
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

// missingFirmware lists firmware files that loaded DVB modules may need and
// that are not installed. Files the kernel log shows failing are "required".
func missingFirmware(failed map[string]bool, r *Report) []Firmware {
	mods := dvbModules()
	var out []Firmware
	seen := map[string]bool{}
	if len(mods) > 0 {
		if _, err := exec.LookPath("modinfo"); err != nil {
			r.Notes = append(r.Notes, "modinfo is not available, so module firmware lists can't be checked.")
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
			if !present || failed[filepath.Base(fw)] {
				out = append(out, Firmware{Module: m, File: fw, Present: present, Required: failed[filepath.Base(fw)]})
			}
		}
	}
	for fw := range failed {
		if !seen[fw] && !firmwarePresent(fw) {
			out = append(out, Firmware{Module: "(kernel log)", File: fw, Required: true})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Required != out[j].Required {
			return out[i].Required
		}
		return out[i].File < out[j].File
	})
	return out
}

func blacklisted() []string {
	var out []string
	for _, dir := range []string{"/etc/modprobe.d", "/lib/modprobe.d", "/usr/lib/modprobe.d", "/run/modprobe.d"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.conf"))
		for _, f := range files {
			fh, err := os.Open(f)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(fh)
			for sc.Scan() {
				fields := strings.Fields(sc.Text())
				if len(fields) >= 2 && fields[0] == "blacklist" && (dvbModule.MatchString(fields[1]) || dvbDriver.MatchString(fields[1])) {
					out = append(out, fmt.Sprintf("%s (%s)", fields[1], f))
				}
			}
			fh.Close()
		}
	}
	return out
}
