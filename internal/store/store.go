package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store guards the configuration State and persists it as JSON.
type Store struct {
	dir   string
	mu    sync.RWMutex
	st    *State
	dirty chan struct{}

	subMu sync.Mutex
	subs  []func()
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, dirty: make(chan struct{}, 1)}
	st := &State{}
	if err := ReadJSON(filepath.Join(dir, "config.json"), st); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	s.st = st
	s.defaults()
	go s.saver()
	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) defaults() {
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
		st.Tuners = map[string]*TunerConfig{}
	}
	if st.Profiles == nil {
		st.Profiles = DefaultProfiles()
	}
	cfg := &st.Settings
	if cfg.ServerName == "" {
		cfg.ServerName = "dvbhub"
	}
	if cfg.DeviceID == "" {
		cfg.DeviceID = NewHDHRDeviceID()
		cfg.SSDP = true
		cfg.EIT = true
		cfg.XMLTVHours = 12
		cfg.EPGDays = 8
		cfg.HDHRProfile = "pass"
	}
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "ffmpeg"
	}
}

// View runs fn with read access to the state. fn must not retain pointers.
func (s *Store) View(fn func(st *State)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(s.st)
}

// Update runs fn with write access and schedules a save if fn succeeds.
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

// OnChange registers a callback run (asynchronously) after every Update.
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
			log.Printf("store: save failed: %v", err)
		}
	}
}

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

func NewID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// NewHDHRDeviceID returns a random 8 hex digit id with a valid HDHomeRun checksum.
func NewHDHRDeviceID() string {
	b := make([]byte, 4)
	rand.Read(b)
	id := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	id &^= 0xf
	lut := [16]uint32{0xa, 0x5, 0xf, 0x6, 0x7, 0xc, 0x1, 0xb, 0x9, 0x2, 0x8, 0xd, 0x4, 0x3, 0xe, 0x0}
	var c uint32
	c ^= lut[id>>28&0xf]
	c ^= id >> 24 & 0xf
	c ^= lut[id>>20&0xf]
	c ^= id >> 16 & 0xf
	c ^= lut[id>>12&0xf]
	c ^= id >> 8 & 0xf
	c ^= lut[id>>4&0xf]
	return fmt.Sprintf("%08X", id|c)
}

// ServiceKey identifies a DVB service independent of our ids.
func ServiceID(muxID string, sid uint16) string { return fmt.Sprintf("%s-%d", muxID, sid) }
