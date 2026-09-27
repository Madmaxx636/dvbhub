package dvb

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"

	"dvbhub/internal/store"
)

// ParseScanFile parses initial tuning data in dvbv5 format ([CHANNEL] blocks,
// as shipped in dtv-scan-tables) or the legacy one-line-per-mux format.
func ParseScanFile(text string) ([]store.Tuning, error) {
	if strings.Contains(text, "DELIVERY_SYSTEM") {
		return parseV5(text)
	}
	return parseLegacy(text)
}

func normDelsys(v string) string {
	switch strings.ToUpper(strings.ReplaceAll(v, "-", "")) {
	case "DVBT":
		return "DVB-T"
	case "DVBT2":
		return "DVB-T2"
	case "DVBC/ANNEX_A", "DVBC/ANNEXA", "DVBC":
		return "DVB-C"
	case "DVBC/ANNEX_B":
		return "DVB-C/B"
	case "DVBC/ANNEX_C":
		return "DVB-C/C"
	case "DVBS":
		return "DVB-S"
	case "DVBS2":
		return "DVB-S2"
	case "ATSC":
		return "ATSC"
	case "ISDBT":
		return "ISDB-T"
	}
	return strings.ToUpper(v)
}

func normMod(v string) string {
	v = strings.ToUpper(v)
	switch v {
	case "PSK/8":
		return "8PSK"
	case "APSK/16":
		return "16APSK"
	case "APSK/32":
		return "32APSK"
	case "VSB/8":
		return "8VSB"
	case "QAM16", "QAM32", "QAM64", "QAM128", "QAM256":
		return "QAM/" + v[3:]
	}
	return v
}

func normPol(v string) string {
	switch strings.ToUpper(v) {
	case "HORIZONTAL", "H":
		return "H"
	case "VERTICAL", "V":
		return "V"
	case "LEFT", "L":
		return "L"
	case "RIGHT", "R":
		return "R"
	}
	return ""
}

func parseV5(text string) ([]store.Tuning, error) {
	var out []store.Tuning
	var cur map[string]string
	flush := func() error {
		if cur == nil {
			return nil
		}
		t := store.Tuning{DeliverySystem: normDelsys(cur["DELIVERY_SYSTEM"]), StreamID: -1}
		f, err := strconv.ParseUint(cur["FREQUENCY"], 10, 64)
		if err != nil {
			return fmt.Errorf("bad FREQUENCY %q", cur["FREQUENCY"])
		}
		if strings.HasPrefix(t.DeliverySystem, "DVB-S") {
			t.FrequencyKHz = uint32(f)
		} else {
			t.FrequencyKHz = uint32(f / 1000)
		}
		if v, err := strconv.ParseUint(cur["BANDWIDTH_HZ"], 10, 32); err == nil {
			t.BandwidthHz = uint32(v)
		}
		if v, err := strconv.ParseUint(cur["SYMBOL_RATE"], 10, 32); err == nil {
			t.SymbolRate = uint32(v)
		}
		if v, err := strconv.Atoi(cur["STREAM_ID"]); err == nil {
			t.StreamID = v
		}
		t.Modulation = normMod(cur["MODULATION"])
		t.FEC = cur["INNER_FEC"]
		if t.FEC == "" {
			t.FEC = cur["CODE_RATE_HP"]
		}
		t.CodeRateLP = cur["CODE_RATE_LP"]
		t.GuardInterval = cur["GUARD_INTERVAL"]
		t.TransmissionMode = cur["TRANSMISSION_MODE"]
		t.Hierarchy = cur["HIERARCHY"]
		t.Polarization = normPol(cur["POLARIZATION"])
		t.Rolloff = cur["ROLLOFF"]
		t.Pilot = cur["PILOT"]
		t.Inversion = cur["INVERSION"]
		out = append(out, t)
		cur = nil
		return nil
	}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if err := flush(); err != nil {
				return nil, err
			}
			cur = map[string]string{}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && cur != nil {
			cur[strings.ToUpper(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

func parseLegacy(text string) ([]store.Tuning, error) {
	var out []store.Tuning
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		num := func(i int) uint64 {
			if i >= len(f) {
				return 0
			}
			v, _ := strconv.ParseUint(f[i], 10, 64)
			return v
		}
		at := func(i int) string {
			if i >= len(f) {
				return "AUTO"
			}
			return strings.ToUpper(f[i])
		}
		switch f[0] {
		case "T", "T2":
			// T freq bw fec_hi fec_lo mod transmission-mode guard-interval hierarchy
			t := store.Tuning{DeliverySystem: "DVB-T", FrequencyKHz: uint32(num(1) / 1000), StreamID: -1,
				FEC: at(3), CodeRateLP: at(4), Modulation: normMod(at(5)), TransmissionMode: at(6), GuardInterval: at(7), Hierarchy: at(8)}
			if f[0] == "T2" {
				t.DeliverySystem = "DVB-T2"
			}
			bw := strings.TrimSuffix(at(2), "MHZ")
			if v, err := strconv.ParseFloat(bw, 64); err == nil {
				t.BandwidthHz = uint32(v * 1e6)
			}
			out = append(out, t)
		case "C":
			// C freq symbol_rate fec modulation
			out = append(out, store.Tuning{DeliverySystem: "DVB-C", FrequencyKHz: uint32(num(1) / 1000), SymbolRate: uint32(num(2)),
				FEC: at(3), Modulation: normMod(at(4)), StreamID: -1})
		case "S", "S1", "S2":
			// S freq(kHz or Hz) pol symbol_rate fec [rolloff mod]
			fr := num(1)
			if fr > 100e6 {
				fr /= 1000
			}
			t := store.Tuning{DeliverySystem: "DVB-S", FrequencyKHz: uint32(fr), Polarization: normPol(at(2)),
				SymbolRate: uint32(num(3)), FEC: at(4), StreamID: -1}
			if f[0] == "S2" {
				t.DeliverySystem = "DVB-S2"
				t.Rolloff = strings.TrimPrefix(at(5), "0.")
				t.Modulation = normMod(at(6))
			}
			out = append(out, t)
		case "A":
			out = append(out, store.Tuning{DeliverySystem: "ATSC", FrequencyKHz: uint32(num(1) / 1000), Modulation: normMod(at(2)), StreamID: -1})
		}
	}
	return out, sc.Err()
}

// DescribeTuning returns a short human label like "DVB-T2 506 MHz".
func DescribeTuning(t store.Tuning) string {
	mhz := float64(t.FrequencyKHz) / 1000
	s := fmt.Sprintf("%s %g MHz", t.DeliverySystem, mhz)
	if t.Polarization != "" {
		s += " " + t.Polarization
	}
	if t.SymbolRate != 0 {
		s += fmt.Sprintf(" %d kS/s", t.SymbolRate/1000)
	}
	if t.StreamID >= 0 {
		s += fmt.Sprintf(" PLP %d", t.StreamID)
	}
	return s
}
