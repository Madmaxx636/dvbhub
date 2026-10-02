package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Store guards the State and saves it as JSON (debounced) after changes.
type Store struct {
	dir   string
	mu    sync.RWMutex
	st    *State
	dirty chan struct{}

	subMu sync.Mutex
	subs  []func()
}

// Open loads <dir>/config.json, creating defaults if it does not exist.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, dirty: make(chan struct{}, 1), st: &State{}}
	if err := ReadJSON(filepath.Join(dir, "config.json"), s.st); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	s.applyDefaults()
	go s.saver()
	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) applyDefaults() {
	st := s.st
	if st.Networks == nil {
		st.Networks = map[string]*Network{}
	}
	if st.Muxes == nil {
		st.Muxes = map[string]*Mux{}
	}
	if st.Services == nil {
		st.Services = map[string]*Service{}
	}
	if st.Channels == nil {
		st.Channels = map[string]*Channel{}
	}
	if st.Tuners == nil {
		st.Tuners = map[string]*Tuner{}
	}
	if st.Profiles == nil {
		st.Profiles = DefaultProfiles()
	}
	def := DefaultSettings()
	set := &st.Settings
	if set.ServerName == "" {
		*set = def
	}
	if set.DeviceID == "" {
		set.DeviceID = NewHDHRDeviceID()
	}
	// Fill fields added in later versions without touching user choices.
	if set.FFmpeg == "" {
		set.FFmpeg = def.FFmpeg
	}
	if set.HWAccel == "" {
		set.HWAccel = def.HWAccel
	}
	if set.UILevel == "" {
		set.UILevel = def.UILevel
	}
	if set.FailoverSecs == 0 {
		set.FailoverSecs = def.FailoverSecs
	}
	if set.MaxOutageSecs == 0 {
		set.MaxOutageSecs = def.MaxOutageSecs
	}
	if set.LingerSecs == 0 {
		set.LingerSecs = def.LingerSecs
	}
	if set.HotplugSecs == 0 {
		set.HotplugSecs = def.HotplugSecs
	}
	if set.GuideDays == 0 {
		set.GuideDays = def.GuideDays
	}
	if set.XMLTVHours == 0 {
		set.XMLTVHours = def.XMLTVHours
	}
	if _, ok := st.Profiles[set.DefaultProfile]; !ok {
		set.DefaultProfile = "original"
		if _, ok := st.Profiles["original"]; !ok {
			st.Profiles["original"] = DefaultProfiles()["original"]
		}
	}
	for _, t := range st.Tuners {
		// Hardware tuners need time for firmware loading on first open.
		if t.TuneTimeout <= 5 && !strings.HasPrefix(t.Key, "virtual") {
			t.TuneTimeout = 15
		}
	}
}

// View runs fn with read access. fn must not keep pointers into the state.
func (s *Store) View(fn func(st *State)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s.st)
}

// Update runs fn with write access; on success the state is saved and change
// listeners are notified.
func (s *Store) Update(fn func(st *State) error) error {
	s.mu.Lock()
	err := fn(s.st)
	s.mu.Unlock()
	if err == nil {
		s.MarkDirty()
		s.notify()
	}
	return err
}

// OnChange registers fn to run (in its own goroutine) after every Update.
func (s *Store) OnChange(fn func()) {
	s.subMu.Lock()
	s.subs = append(s.subs, fn)
	s.subMu.Unlock()
}

func (s *Store) notify() {
	s.subMu.Lock()
	subs := append([]func(){}, s.subs...)
	s.subMu.Unlock()
	for _, fn := range subs {
		go fn()
	}
}

func (s *Store) MarkDirty() {
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

func (s *Store) saver() {
	for range s.dirty {
		time.Sleep(500 * time.Millisecond)
		if err := s.Save(); err != nil {
			log.Printf("config: save failed: %v", err)
		}
	}
}

// Save writes the state now.
func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return WriteJSON(filepath.Join(s.dir, "config.json"), s.st)
}

// ReadJSON decodes a JSON file into v.
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// WriteJSON atomically writes v as indented JSON.
func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NewID returns a short random identifier.
func NewID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ServiceID is the stable id of a service on a mux.
func ServiceID(muxID string, sid uint16) string { return fmt.Sprintf("%s-%d", muxID, sid) }

// NewHDHRDeviceID returns a random 8-hex-digit id with a valid HDHomeRun checksum.
func NewHDHRDeviceID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return HDHRDeviceID(uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]))
}

// HDHRDeviceID fixes up the checksum nibble of id.
func HDHRDeviceID(id uint32) string {
	id &^= 0xf
	lut := [16]uint32{0xa, 0x5, 0xf, 0x6, 0x7, 0xc, 0x1, 0xb, 0x9, 0x2, 0x8, 0xd, 0x4, 0x3, 0xe, 0x0}
	c := lut[id>>28&0xf] ^ id>>24&0xf ^ lut[id>>20&0xf] ^ id>>16&0xf ^ lut[id>>12&0xf] ^ id>>8&0xf ^ lut[id>>4&0xf]
	return fmt.Sprintf("%08X", id|c)
}
