// Package hardware finds TV tuner devices, their drivers and firmware, and
// passes driver/firmware install requests to the optional host installer.
package hardware

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

// Firmware is a firmware file a loaded DVB driver can ask for.
type Firmware struct {
	Module  string `json:"module"`
	File    string `json:"file"`
	Present bool   `json:"present"`
	Needed  bool   `json:"needed"` // the kernel log shows the driver failed to load it
}

// Report is the result of a hardware scan.
type Report struct {
	At          time.Time  `json:"at"`
	Kernel      string     `json:"kernel"`
	Container   bool       `json:"container"`
	SecureBoot  string     `json:"secureBoot"` // enabled, disabled, unknown
	Adapters    []string   `json:"adapters"`
	Devices     []Device   `json:"devices"`
	Firmware    []Firmware `json:"firmware"` // needed files (missing or failing) first, then optional missing ones
	Blacklisted []string   `json:"blacklisted"`
	KernelLog   []string   `json:"kernelLog"`
	LogReadable bool       `json:"kernelLogReadable"`
	Notes       []string   `json:"notes"`
	Summary     string     `json:"summary"`
	Problem     bool       `json:"problem"` // something needs the user's attention
	Actions     []string   `json:"actions"` // suggested installer actions
}

type brand struct {
	name string
	hint string
	tbs  bool
}

// USB vendor ids of TV tuner makers (Realtek 0bda is matched by product below).
var usbVendors = map[string]brand{
	"2040": {"Hauppauge", "Hauppauge tuners usually just need firmware files (Install firmware).", false},
	"734c": {"TBS", "TBS tuners often need the TBS driver package (Build TBS drivers).", true},
	"0572": {"DVBSky / Conexant", "DVBSky tuners work with the kernel's drivers plus firmware.", false},
	"1f4d": {"Geniatech / MyGica", "", false},
	"0ccd": {"TerraTec", "", false},
	"2013": {"PCTV Systems", "PCTV tuners usually need firmware files.", false},
	"2304": {"Pinnacle", "", false},
	"048d": {"ITE (IT913x/IT9135)", "IT913x sticks need the dvb-usb-it9135 firmware.", false},
	"15f4": {"HanfTek", "", false},
	"1b80": {"Afatech", "", false},
	"07ca": {"AVerMedia", "", false},
	"185b": {"Compro", "", false},
	"0413": {"Leadtek", "", false},
	"0fe9": {"DViCO", "", false},
	"187f": {"Siano", "Siano tuners need the sms1xxx firmware.", false},
	"1d19": {"Dexatek", "", false},
	"13d3": {"AzureWave", "", false},
	"1554": {"Prolink / PixelView", "", false},
	"eb1a": {"Empia (em28xx)", "", false},
	"1164": {"YUAN", "", false},
}

var realtekDVB = map[string]bool{"2832": true, "2838": true}

// PCI vendor or subsystem-vendor ids of TV tuner makers.
var pciVendors = map[string]brand{
	"544d": {"TBS", "TBS cards often need the TBS driver package (Build TBS drivers).", true},
	"0070": {"Hauppauge", "Hauppauge cards usually just need firmware files.", false},
	"4254": {"DVBSky", "DVBSky cards work with the kernel's drivers plus firmware.", false},
	"dd01": {"Digital Devices", "Digital Devices cards use the kernel's ddbridge driver.", false},
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
}

func NewDetector(dataDir string) *Detector { return &Detector{dataDir: dataDir} }

func readTrim(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

// Report returns a hardware report, cached for 10 s unless force is set.
func (d *Detector) Report(force bool) Report {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.report == nil || force || time.Since(d.report.At) > 10*time.Second {
		r := detect()
		d.report = &r
	}
	return *d.report
}

func detect() Report {
	r := Report{At: time.Now(), Kernel: readTrim("/proc/sys/kernel/osrelease"), SecureBoot: secureBoot(),
		Adapters: []string{}, Devices: []Device{}, Firmware: []Firmware{}, Blacklisted: []string{}, KernelLog: []string{}, Notes: []string{}, Actions: []string{}}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		r.Container = true
		r.Notes = append(r.Notes, "dvbhub runs in a container: drivers and firmware are installed on the host (the host installer does that).")
	}
	adapterDev := map[string][]string{} // sysfs device path -> adapters
	fes, _ := filepath.Glob("/sys/class/dvb/dvb*.frontend*")
	seenAdapter := map[string]bool{}
	for _, fe := range fes {
		var a, f int
		if _, err := fmt.Sscanf(filepath.Base(fe), "dvb%d.frontend%d", &a, &f); err != nil {
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
	r.LogReadable = klog != nil
	needed := neededFirmware(klog)
	fwAny := regexp.MustCompile(`(?i)[\w./+-]+\.(?:fw|bin|inp)\b`)
	for _, line := range klog {
		l := strings.ToLower(line)
		dvbish := strings.Contains(l, "dvb") || strings.Contains(l, "frontend") || strings.Contains(l, "demod") ||
			strings.Contains(l, "tuner") || dvbDriver.MatchString(l)
		if dvbish || (strings.Contains(l, "firmware") && fwAny.MatchString(line)) {
			r.KernelLog = append(r.KernelLog, line)
		}
	}
	if len(r.KernelLog) > 60 {
		r.KernelLog = r.KernelLog[len(r.KernelLog)-60:]
	}
	if klog == nil {
		r.Notes = append(r.Notes, "The kernel log can't be read from here, so firmware load errors can't be confirmed. Files marked optional may or may not be needed.")
	}
	r.Firmware = firmwareList(needed, &r)
	r.Blacklisted = blacklisted()
	summarize(&r)
	return r
}

func summarize(r *Report) {
	needTBS := false
	notWorking := 0
	for _, dev := range r.Devices {
		if dev.Status == "working" {
			continue
		}
		notWorking++
		if strings.HasPrefix(dev.Brand, "TBS") {
			needTBS = true
		}
	}
	needed := 0
	for _, f := range r.Firmware {
		if f.Needed {
			needed++
		}
	}
	switch {
	case len(r.Devices) == 0 && len(r.Adapters) == 0:
		r.Summary = "No TV tuner hardware found. Check that it is plugged in (USB) or seated (PCIe)."
		r.Problem = true
	case notWorking == 0 && needed == 0:
		r.Summary = fmt.Sprintf("All good: %d tuner adapter(s) ready.", len(r.Adapters))
	case needed > 0:
		r.Summary = fmt.Sprintf("%d firmware file(s) a driver tried to load are missing. Install firmware, then replug the tuner or restart the computer.", needed)
		r.Problem = true
	default:
		r.Summary = fmt.Sprintf("%d tuner device(s) found, %d not working yet.", len(r.Devices), notWorking)
		r.Problem = true
	}
	if needed > 0 || notWorking > 0 {
		r.Actions = append(r.Actions, "firmware")
	}
	if needTBS {
		r.Actions = append(r.Actions, "tbs")
	}
	if len(r.Blacklisted) > 0 {
		r.Notes = append(r.Notes, "Some DVB driver modules are blacklisted (often by rtl-sdr packages). Remove those lines to use them as TV tuners.")
	}
	if needTBS && r.SecureBoot == "enabled" {
		r.Notes = append(r.Notes, "Secure Boot is on: self-built TBS driver modules won't load unless you sign them or turn Secure Boot off.")
	}
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
		dev.Hints = append(dev.Hints, "A driver ("+dev.Driver+") is loaded but created no TV adapter. Usually missing firmware, or an unsupported tuner revision.")
	default:
		dev.Status = "no-driver"
		dev.Hints = append(dev.Hints, "No driver is attached. It may need a newer kernel, extra drivers (e.g. TBS) or a module that is blacklisted.")
	}
}

func usbDevices(adapterDev map[string][]string) []Device {
	out := []Device{}
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
		finishDevice(&dev, real, adapterDev)
		if b.hint != "" && dev.Status != "working" {
			dev.Hints = append(dev.Hints, b.hint)
		}
		out = append(out, dev)
	}
	return out
}

func pciDevices(adapterDev map[string][]string) []Device {
	out := []Device{}
	devs, _ := filepath.Glob("/sys/bus/pci/devices/*")
	var names map[string]string
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
		if names == nil {
			names = pciNames()
		}
		name := names[vid+":"+did]
		if name == "" {
			name = "PCI multimedia device"
		}
		dev := Device{Bus: "pci", ID: vid + ":" + did, Name: name, Brand: b.name, Driver: driver}
		if dev.Brand == "" {
			dev.Brand = "Unknown"
		}
		finishDevice(&dev, real, adapterDev)
		if b.hint != "" && dev.Status != "working" {
			dev.Hints = append(dev.Hints, b.hint)
		}
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
				return out // device classes follow the vendor list
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

// kernelLog returns kernel messages, or nil if they can't be read.
func kernelLog() []string {
	out, err := run("dmesg")
	if err != nil || strings.TrimSpace(out) == "" {
		out, err = run("journalctl", "-k", "-b", "--no-pager", "-q", "-o", "cat", "-n", "3000")
		if err != nil || strings.TrimSpace(out) == "" {
			return nil
		}
	}
	return strings.Split(strings.TrimSpace(out), "\n")
}

func blacklisted() []string {
	out := []string{}
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
