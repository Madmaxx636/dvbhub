package scan

import (
	"errors"
	"fmt"

	"dvbhub/internal/config"
	"dvbhub/internal/dvb"
)

// FindPlan returns a built-in channel plan by id.
func FindPlan(id string) (dvb.Plan, bool) {
	for _, p := range dvb.Plans() {
		if p.ID == id {
			return p, true
		}
	}
	return dvb.Plan{}, false
}

// ApplyPlan adds the plan's channels that a network doesn't have yet as muxes
// and returns how many were added.
func ApplyPlan(st *config.Store, netID, planID string) (int, error) {
	plan, ok := FindPlan(planID)
	if !ok {
		return 0, fmt.Errorf("unknown channel plan %q", planID)
	}
	added := 0
	err := st.Update(func(state *config.State) error {
		net, ok := state.Networks[netID]
		if !ok {
			return errors.New("network not found")
		}
		net.Plan = plan.ID
		for _, ch := range plan.Channels {
			if hasFrequency(state, netID, ch.Tuning) {
				continue
			}
			m := &config.Mux{ID: config.NewID(), NetworkID: netID, Label: ch.Label, Tuning: ch.Tuning, Enabled: true,
				Scan: config.ScanState{Status: "new"}}
			state.Muxes[m.ID] = m
			added++
		}
		return nil
	})
	return added, err
}

// AddTunings adds muxes (from a scan table) that a network doesn't have yet.
func AddTunings(st *config.Store, netID string, tunings []dvb.Tuning) ([]string, error) {
	var added []string
	err := st.Update(func(state *config.State) error {
		if _, ok := state.Networks[netID]; !ok {
			return errors.New("network not found")
		}
		for _, t := range tunings {
			if hasFrequency(state, netID, t) {
				continue
			}
			m := &config.Mux{ID: config.NewID(), NetworkID: netID, Tuning: t, Enabled: true, Scan: config.ScanState{Status: "new"}}
			state.Muxes[m.ID] = m
			added = append(added, m.ID)
		}
		return nil
	})
	return added, err
}

func hasFrequency(state *config.State, netID string, t dvb.Tuning) bool {
	for _, m := range state.Muxes {
		if m.NetworkID != netID {
			continue
		}
		d := int64(m.Tuning.FrequencyKHz) - int64(t.FrequencyKHz)
		if d < 0 {
			d = -d
		}
		if d <= 500 && m.Tuning.Polarization == t.Polarization && m.Tuning.StreamID == t.StreamID {
			return true
		}
	}
	return false
}

// SetupRequest is the one-click "find my channels" request.
type SetupRequest struct {
	Type    string      `json:"type"` // atsc, cable, dvbt, ...
	Plan    string      `json:"plan"` // built-in plan id
	Name    string      `json:"name"`
	AutoMap bool        `json:"autoMap"`
	Map     *MapOptions `json:"map,omitempty"` // nil = DefaultMapOptions
}

// Setup finds or creates a network for the plan, adds its channels and
// scans them all. Returns the network id and the number of muxes queued.
func (s *Scanner) Setup(req SetupRequest) (string, int, error) {
	plan, ok := FindPlan(req.Plan)
	if !ok {
		return "", 0, fmt.Errorf("unknown channel plan %q", req.Plan)
	}
	if req.Type == "" {
		req.Type = plan.Type
	}
	var netID string
	s.st.View(func(state *config.State) {
		for id, n := range state.Networks {
			if n.Type == req.Type && (n.Plan == plan.ID || n.Plan == "") && netID == "" {
				netID = id
			}
		}
	})
	if netID == "" {
		name := req.Name
		if name == "" {
			name = plan.Name
		}
		netID = config.NewID()
		n := &config.Network{ID: netID, Name: name, Type: req.Type, Plan: plan.ID, DiscoverMuxes: req.Type != "atsc" && req.Type != "cable"}
		s.st.Update(func(state *config.State) error {
			state.Networks[netID] = n
			return nil
		})
	}
	if _, err := ApplyPlan(s.st, netID, plan.ID); err != nil {
		return "", 0, err
	}
	opt := DefaultMapOptions()
	if req.Map != nil {
		opt = *req.Map
	}
	n, err := s.ScanNetwork(netID, req.AutoMap, opt)
	return netID, n, err
}
