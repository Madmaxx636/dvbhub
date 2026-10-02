package scan

import (
	"fmt"
	"log"
	"strings"
	"time"

	"dvbhub/internal/config"
	"dvbhub/internal/dvb"
	ts "dvbhub/internal/mpegts"
	"dvbhub/internal/tuners"
)

func (s *Scanner) setMuxStatus(muxID, status string) {
	s.st.Update(func(state *config.State) error {
		if m, ok := state.Muxes[muxID]; ok {
			m.Scan.Status = status
		}
		return nil
	})
}

// finish stores the result of scanning a mux and returns how many TV/radio
// services it carries. Muxes announced in the NIT are added and queued.
func (s *Scanner) finish(muxID string, res *result, scanErr error, sig *dvb.SignalSnap) int {
	var discovered []string
	found := 0
	s.st.Update(func(state *config.State) error {
		mux, ok := state.Muxes[muxID]
		if !ok {
			return nil
		}
		mux.Scan = config.ScanState{Status: statusOf(res, scanErr), At: time.Now()}
		if sig != nil {
			mux.Signal = sig
		}
		if scanErr != nil {
			if mux.Scan.Status == "fail" {
				mux.Scan.Error = scanErr.Error()
				log.Printf("scan: %s failed: %v", muxLabel(mux), scanErr)
			}
			return nil
		}
		found = storeServices(state, mux, res)
		discovered = discoverMuxes(state, mux, res)
		log.Printf("scan: %s ok, %d services", muxLabel(mux), found)
		return nil
	})
	if len(discovered) > 0 {
		s.mu.Lock()
		for _, j := range s.jobs {
			if j.Finished == nil && j.NetworkID == s.networkOf(muxID) {
				for _, id := range discovered {
					j.pending[id] = true
					j.Total++
				}
			}
		}
		s.mu.Unlock()
		s.Enqueue(discovered...)
	}
	return found
}

func (s *Scanner) networkOf(muxID string) string {
	var id string
	s.st.View(func(state *config.State) {
		if m, ok := state.Muxes[muxID]; ok {
			id = m.NetworkID
		}
	})
	return id
}

func muxLabel(m *config.Mux) string { return tuners.MuxLabel(*m) }

func isVideo(kind string) bool { return kind == "MPEG2VIDEO" || kind == "H264" || kind == "HEVC" }

func isAudio(kind string) bool {
	switch kind {
	case "MPEG2AUDIO", "AAC", "AAC-LATM", "AC3", "EAC3", "AC4":
		return true
	}
	return false
}

// storeServices creates or updates the services of a scanned mux. Services
// that have disappeared are removed unless a channel uses them.
func storeServices(state *config.State, mux *config.Mux, res *result) int {
	mux.TSID = res.pat.TSID
	sdtBySID := map[uint16]ts.SDTService{}
	if res.sdt != nil {
		mux.ONID = res.sdt.ONID
		for _, sv := range res.sdt.Services {
			sdtBySID[sv.SID] = sv
		}
	}
	// A VCT can also list other transport streams' channels; use the entries
	// for this one, or (for stations that send a wrong TSID) the entries whose
	// program numbers are in this mux's PAT.
	vctByProg := map[uint16]ts.VCTChannel{}
	for _, vc := range res.vct {
		if vc.TSID == mux.TSID {
			vctByProg[vc.Program] = vc
		}
	}
	if len(vctByProg) == 0 {
		for _, vc := range res.vct {
			if _, inPAT := res.pat.Programs[vc.Program]; inPAT {
				if _, dup := vctByProg[vc.Program]; !dup {
					vctByProg[vc.Program] = vc
				}
			}
		}
	}
	if net := state.Networks[mux.NetworkID]; net != nil && res.nit != nil {
		net.NetworkID = res.nit.NetworkID
		if mux.ONID == 0 {
			for _, tr := range res.nit.Transports {
				if tr.TSID == mux.TSID {
					mux.ONID = tr.ONID
				}
			}
		}
	}

	now := time.Now()
	seen := map[string]bool{}
	found := 0
	for prog, pmtPID := range res.pat.Programs {
		if prog == 0 {
			continue
		}
		id := config.ServiceID(mux.ID, prog)
		seen[id] = true
		svc, exists := state.Services[id]
		if !exists {
			svc = &config.Service{ID: id, MuxID: mux.ID, SID: prog, Enabled: true}
			state.Services[id] = svc
		}
		svc.LastSeen = now
		svc.PMTPID = pmtPID
		hasVideo, hasAudio := false, false
		if pmt := res.pmts[prog]; pmt != nil {
			svc.PCRPID = pmt.PCRPID
			svc.Scrambled = pmt.Scrambled
			svc.Streams = svc.Streams[:0]
			for _, es := range pmt.Streams {
				svc.Streams = append(svc.Streams, config.Stream{PID: es.PID, StreamType: es.StreamType, Kind: es.Kind, Lang: es.Lang})
				hasVideo = hasVideo || isVideo(es.Kind)
				hasAudio = hasAudio || isAudio(es.Kind)
			}
		}
		svc.Kind = ""
		if sv, ok := sdtBySID[prog]; ok {
			svc.Name, svc.Provider, svc.Type = sv.Name, sv.Provider, sv.Type
			svc.Kind = ts.ServiceKind(sv.Type)
			if sv.FreeCA {
				svc.Scrambled = true
			}
		}
		vc, inVCT := vctByProg[prog]
		if inVCT {
			if vc.ShortName != "" {
				svc.Name = vc.ShortName
			}
			svc.Major, svc.Minor, svc.SourceID = vc.Major, vc.Minor, vc.SourceID
			switch {
			case vc.Hidden || vc.ServiceType == 0x04:
				svc.Kind = "other"
			case vc.ServiceType == 0x03:
				svc.Kind = "radio"
			case vc.ServiceType == 0x02:
				svc.Kind = "tv"
			}
			if vc.AccessControlled {
				svc.Scrambled = true
			}
		}
		// Fall back to the streams when the tables don't say, say "other"
		// for something that clearly has video or audio, or say TV for a
		// service without any video.
		if svc.Kind == "" || (svc.Kind == "other" && !(inVCT && (vc.Hidden || vc.ServiceType == 0x04))) ||
			(svc.Kind == "tv" && !hasVideo && hasAudio) {
			switch {
			case hasVideo:
				svc.Kind = "tv"
			case hasAudio:
				svc.Kind = "radio"
			default:
				svc.Kind = "other"
			}
		}
		if strings.TrimSpace(svc.Name) == "" {
			svc.Name = fmt.Sprintf("Service %d", prog)
		}
		if svc.Kind != "other" {
			found++
		}
	}
	mapped := map[string]bool{}
	for _, c := range state.Channels {
		for _, sid := range c.Services {
			mapped[sid] = true
		}
	}
	for id, svc := range state.Services {
		if svc.MuxID == mux.ID && !seen[id] && !mapped[id] {
			delete(state.Services, id)
		}
	}
	if res.nit != nil {
		applyLCNs(state, mux.NetworkID, res.nit)
	}
	return found
}

// applyLCNs copies logical channel numbers from a NIT to the services of
// whichever muxes carry the listed transport streams.
func applyLCNs(state *config.State, netID string, nit *ts.NIT) {
	for _, tr := range nit.Transports {
		if len(tr.LCN) == 0 {
			continue
		}
		for _, m := range state.Muxes {
			if m.NetworkID != netID || m.TSID != tr.TSID || (m.ONID != 0 && m.ONID != tr.ONID) {
				continue
			}
			for sid, lcn := range tr.LCN {
				if svc, ok := state.Services[config.ServiceID(m.ID, sid)]; ok && lcn > 0 {
					svc.Major, svc.Minor = lcn, 0
				}
			}
		}
	}
}

// discoverMuxes adds muxes announced in the NIT that the network doesn't have yet.
func discoverMuxes(state *config.State, mux *config.Mux, res *result) []string {
	net := state.Networks[mux.NetworkID]
	if res.nit == nil || net == nil || !net.DiscoverMuxes {
		return nil
	}
	var added []string
	for _, tr := range res.nit.Transports {
		d := tr.Delivery
		if d == nil || d.FrequencyKHz == 0 || !deliveryMatchesNetwork(d.System, net.Type) || findMux(state, net.ID, d) != nil {
			continue
		}
		m := &config.Mux{ID: config.NewID(), NetworkID: net.ID, Enabled: true, TSID: tr.TSID, ONID: tr.ONID,
			Scan: config.ScanState{Status: "new"},
			Tuning: dvb.Tuning{DeliverySystem: d.System, FrequencyKHz: d.FrequencyKHz, BandwidthHz: d.BandwidthHz,
				SymbolRate: d.SymbolRate, Modulation: d.Modulation, FEC: d.FEC, CodeRateLP: d.CodeRateLP,
				GuardInterval: d.GuardInterval, TransmissionMode: d.TransmissionMode, Hierarchy: d.Hierarchy,
				Polarization: d.Polarization, Rolloff: d.Rolloff, StreamID: d.StreamID}}
		state.Muxes[m.ID] = m
		added = append(added, m.ID)
		log.Printf("scan: found new mux %s via the NIT", dvb.DescribeTuning(m.Tuning))
	}
	return added
}

func deliveryMatchesNetwork(sys, netType string) bool {
	switch netType {
	case "dvbt":
		return strings.HasPrefix(sys, "DVB-T")
	case "dvbc":
		return strings.HasPrefix(sys, "DVB-C")
	case "dvbs":
		return strings.HasPrefix(sys, "DVB-S")
	}
	return false
}

// findMux returns the network's mux on (about) the same frequency, if any.
func findMux(state *config.State, netID string, d *ts.Delivery) *config.Mux {
	for _, m := range state.Muxes {
		if m.NetworkID != netID {
			continue
		}
		diff := int64(m.Tuning.FrequencyKHz) - int64(d.FrequencyKHz)
		if diff < 0 {
			diff = -diff
		}
		tol := int64(1000)
		if strings.HasPrefix(d.System, "DVB-S") {
			tol = 4000
			if m.Tuning.Polarization != d.Polarization {
				continue
			}
		}
		if diff <= tol && (d.StreamID < 0 || m.Tuning.StreamID < 0 || m.Tuning.StreamID == d.StreamID) {
			return m
		}
	}
	return nil
}
