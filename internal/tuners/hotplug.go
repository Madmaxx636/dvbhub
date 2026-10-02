package tuners

import (
	"path/filepath"
	"strings"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/dvb"
)

// watchHardware runs discovery once at startup and again whenever the set of
// frontend device nodes changes (a USB tuner plugged in or removed). Listing
// /dev/dvb is cheap, so this costs next to nothing while nothing changes.
func (m *Manager) watchHardware() {
	known := ""
	for {
		nodes, _ := filepath.Glob("/dev/dvb/adapter*/frontend*")
		current := strings.Join(nodes, "|")
		if !m.HardwareScanned() || current != known {
			known = current
			m.setHardware(dvb.Discover())
		}
		interval := 10
		m.st.View(func(st *config.State) {
			if st.Settings.HotplugSecs > 0 {
				interval = st.Settings.HotplugSecs
			}
		})
		time.Sleep(time.Duration(interval) * time.Second)
	}
}
