package scan

import (
	"sort"
	"strconv"
	"strings"

	"dvbhub/internal/config"
)

// MapOptions controls automatic channel creation.
type MapOptions struct {
	ServiceIDs    []string `json:"serviceIds"` // empty = all enabled services
	IncludeRadio  bool     `json:"includeRadio"`
	SkipScrambled bool     `json:"skipScrambled"`
	MergeByName   bool     `json:"mergeByName"` // same channel on another mux becomes a backup service
}

// DefaultMapOptions are used by the one-click setup.
func DefaultMapOptions() MapOptions {
	return MapOptions{IncludeRadio: false, SkipScrambled: true, MergeByName: true}
}

type number struct{ major, minor int }

// mergeKey identifies "the same channel" received on several muxes: its name,
// plus its number when the broadcaster sends one (so two stations that
// happen to share a short name stay separate).
func mergeKey(name string, n number) string {
	k := strings.ToLower(strings.TrimSpace(name))
	if n.major > 0 {
		k += "|" + strconv.Itoa(n.major) + "." + strconv.Itoa(n.minor)
	}
	return k
}

// MapServices creates channels for services that are not mapped yet and
// returns how many channels were created and how many services were added
// to existing channels as backups.
func MapServices(st *config.Store, opt MapOptions) (created, merged int) {
	st.Update(func(state *config.State) error {
		mapped := map[string]bool{}
		used := map[number]bool{}
		byKey := map[string]*config.Channel{}
		maxNum := 0
		for _, c := range state.Channels {
			for _, s := range c.Services {
				mapped[s] = true
			}
			used[number{c.Number, c.Minor}] = true
			maxNum = max(maxNum, c.Number)
			byKey[mergeKey(c.Name, number{c.Number, c.Minor})] = c
			byKey[mergeKey(c.Name, number{})] = c
		}
		var svcs []*config.Service
		if len(opt.ServiceIDs) > 0 {
			for _, id := range opt.ServiceIDs {
				if s, ok := state.Services[id]; ok {
					svcs = append(svcs, s)
				}
			}
		} else {
			for _, s := range state.Services {
				svcs = append(svcs, s)
			}
		}
		// Numbered services first, in number order, so they keep their numbers.
		sort.Slice(svcs, func(i, j int) bool {
			a, b := svcs[i], svcs[j]
			if (a.Major == 0) != (b.Major == 0) {
				return a.Major != 0
			}
			if a.Major != b.Major {
				return a.Major < b.Major
			}
			if a.Minor != b.Minor {
				return a.Minor < b.Minor
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return signalScore(state, a) > signalScore(state, b)
		})
		explicit := len(opt.ServiceIDs) > 0
		for _, s := range svcs {
			if mapped[s.ID] || !s.Enabled || (s.Kind == "other" && !explicit) || (s.Kind == "radio" && !opt.IncludeRadio && !explicit) ||
				(s.Scrambled && opt.SkipScrambled) {
				continue
			}
			n := number{s.Major, s.Minor}
			if opt.MergeByName {
				if c := byKey[mergeKey(s.Name, n)]; c != nil {
					c.Services = append(c.Services, s.ID)
					mapped[s.ID] = true
					merged++
					continue
				}
			}
			if n.major <= 0 || used[n] {
				n = number{maxNum + 1, 0}
			}
			used[n] = true
			maxNum = max(maxNum, n.major)
			c := &config.Channel{ID: config.NewID(), Number: n.major, Minor: n.minor, Name: s.Name, Enabled: true,
				Services: []string{s.ID}, Radio: s.Kind == "radio"}
			state.Channels[c.ID] = c
			byKey[mergeKey(c.Name, number{s.Major, s.Minor})] = c
			mapped[s.ID] = true
			created++
		}
		return nil
	})
	return
}

// signalScore orders duplicate services so the best-received one becomes
// the channel's main service and the others its backups.
func signalScore(state *config.State, s *config.Service) float64 {
	m, ok := state.Muxes[s.MuxID]
	if !ok || m.Signal == nil {
		return -1
	}
	sc := float64(m.Signal.Bars) * 100
	if m.Signal.SNRdB != nil {
		sc += *m.Signal.SNRdB
	} else {
		sc += m.Signal.StrengthPct / 10
	}
	return sc
}

// Renumber gives channels consecutive numbers from start, in their current
// order.
func Renumber(st *config.Store, ids []string, start int) {
	st.Update(func(state *config.State) error {
		n := start
		for _, id := range ids {
			if c, ok := state.Channels[id]; ok {
				c.Number, c.Minor = n, 0
				n++
			}
		}
		return nil
	})
}
