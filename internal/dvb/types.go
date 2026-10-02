package dvb

import (
	"fmt"
	"time"
)

// Tuning describes how to tune one multiplex (one RF channel). Frequencies
// are always in kHz.
type Tuning struct {
	DeliverySystem   string `json:"delsys"` // ATSC, DVB-C/B (US cable QAM), DVB-T, DVB-T2, DVB-C, DVB-S, DVB-S2
	FrequencyKHz     uint32 `json:"frequency"`
	BandwidthHz      uint32 `json:"bandwidth,omitempty"`
	SymbolRate       uint32 `json:"symbolRate,omitempty"`
	Modulation       string `json:"modulation,omitempty"`
	FEC              string `json:"fec,omitempty"`
	CodeRateLP       string `json:"fecLP,omitempty"`
	TransmissionMode string `json:"transmissionMode,omitempty"`
	GuardInterval    string `json:"guardInterval,omitempty"`
	Hierarchy        string `json:"hierarchy,omitempty"`
	Polarization     string `json:"polarization,omitempty"` // H, V, L, R
	Rolloff          string `json:"rolloff,omitempty"`
	Pilot            string `json:"pilot,omitempty"`
	Inversion        string `json:"inversion,omitempty"`
	StreamID         int    `json:"streamId"` // PLP id / ISI, -1 for none
}

// SatInput configures how a tuner reaches a satellite (LNB + DiSEqC switch).
type SatInput struct {
	LNB        string `json:"lnb"`        // universal, single, circular, none
	LOFLow     uint32 `json:"lofLow"`     // kHz
	LOFHigh    uint32 `json:"lofHigh"`    // kHz
	Switch     uint32 `json:"switch"`     // kHz
	DiseqcPort int    `json:"diseqcPort"` // 0 = none, 1..4 committed port
}

// SignalSnap is a stored signal reading.
type SignalSnap struct {
	Locked      bool      `json:"locked"`
	StrengthPct float64   `json:"strengthPct"`
	StrengthDBm *float64  `json:"strengthDbm,omitempty"`
	SNRdB       *float64  `json:"snrDb,omitempty"`
	SNRPct      float64   `json:"snrPct"`
	Bars        int       `json:"bars"`
	Quality     string    `json:"quality"`
	At          time.Time `json:"at"`
	Live        bool      `json:"live,omitempty"`
}

// ---- built-in channel plans ----

// Plan is a named list of frequencies to scan.
type Plan struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"` // network type it applies to: atsc, cable, dvbt, dvbc
	Channels []PlanCh `json:"channels"`
}

// PlanCh is one RF channel of a plan.
type PlanCh struct {
	Label  string `json:"label"` // e.g. "RF 14"
	Tuning Tuning `json:"tuning"`
}

func rf(label string, t Tuning) PlanCh { return PlanCh{Label: label, Tuning: t} }

// Plans returns the built-in channel plans.
func Plans() []Plan {
	var usAir, usCable, euT, auT []PlanCh
	atsc := func(n int, centerMHz float64) {
		// Linux ATSC tuners are tuned to the channel centre plus the 8VSB
		// pilot offset used by dtv-scan-tables (28.615 kHz).
		usAir = append(usAir, rf(fmt.Sprintf("RF %d", n), Tuning{DeliverySystem: "ATSC",
			FrequencyKHz: uint32(centerMHz*1000) + 28, Modulation: "8VSB", StreamID: -1}))
	}
	for _, c := range []struct {
		n int
		f float64
	}{{2, 57}, {3, 63}, {4, 69}, {5, 79}, {6, 85}} {
		atsc(c.n, c.f)
	}
	for n := 7; n <= 13; n++ {
		atsc(n, 177+float64(n-7)*6)
	}
	for n := 14; n <= 36; n++ { // the 2020 US/Canada repack ended UHF TV at RF 36
		atsc(n, 473+float64(n-14)*6)
	}

	qam := func(n int, centerMHz float64) {
		usCable = append(usCable, rf(fmt.Sprintf("Cable %d", n), Tuning{DeliverySystem: "DVB-C/B",
			FrequencyKHz: uint32(centerMHz * 1000), Modulation: "QAM/256", StreamID: -1}))
	}
	for _, c := range []struct {
		n int
		f float64
	}{{2, 57}, {3, 63}, {4, 69}, {5, 79}, {6, 85}} {
		qam(c.n, c.f)
	}
	for n := 7; n <= 13; n++ {
		qam(n, 177+float64(n-7)*6)
	}
	for n := 14; n <= 22; n++ {
		qam(n, 123+float64(n-14)*6)
	}
	for n := 23; n <= 94; n++ {
		qam(n, 219+float64(n-23)*6)
	}
	for n := 95; n <= 99; n++ {
		qam(n, 93+float64(n-95)*6)
	}
	for n := 100; n <= 158; n++ {
		qam(n, 651+float64(n-100)*6)
	}

	dvbt := func(list *[]PlanCh, label string, mhz float64, bw uint32) {
		*list = append(*list, rf(label, Tuning{DeliverySystem: "DVB-T", FrequencyKHz: uint32(mhz * 1000),
			BandwidthHz: bw, Modulation: "AUTO", FEC: "AUTO", CodeRateLP: "AUTO", TransmissionMode: "AUTO",
			GuardInterval: "AUTO", Hierarchy: "NONE", StreamID: -1}))
	}
	for n := 5; n <= 12; n++ {
		dvbt(&euT, fmt.Sprintf("E%d", n), 177.5+float64(n-5)*7, 7_000_000)
	}
	for n := 21; n <= 48; n++ { // UHF 49-69 was cleared for mobile (700 MHz band)
		dvbt(&euT, fmt.Sprintf("E%d", n), 474+float64(n-21)*8, 8_000_000)
	}
	for n := 6; n <= 12; n++ {
		dvbt(&auT, fmt.Sprintf("VHF %d", n), 177.5+float64(n-6)*7, 7_000_000)
	}
	for n := 27; n <= 51; n++ {
		dvbt(&auT, fmt.Sprintf("UHF %d", n), 522.5+float64(n-27)*7, 7_000_000)
	}
	return []Plan{
		{ID: "us-atsc", Name: "USA / Canada antenna (ATSC, RF 2-36)", Type: "atsc", Channels: usAir},
		{ID: "us-cable", Name: "USA cable (clear QAM, 2-158)", Type: "cable", Channels: usCable},
		{ID: "eu-dvbt", Name: "Europe antenna (DVB-T/T2)", Type: "dvbt", Channels: euT},
		{ID: "au-dvbt", Name: "Australia antenna (DVB-T)", Type: "dvbt", Channels: auT},
	}
}

// DelsysForNetwork lists the delivery systems a network type uses.
func DelsysForNetwork(netType string) []string {
	switch netType {
	case "atsc":
		return []string{"ATSC"}
	case "cable":
		return []string{"DVB-C/B"}
	case "dvbt":
		return []string{"DVB-T", "DVB-T2"}
	case "dvbc":
		return []string{"DVB-C"}
	case "dvbs":
		return []string{"DVB-S", "DVB-S2"}
	}
	return nil
}
