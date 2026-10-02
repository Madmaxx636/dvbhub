package mpegts

import (
	"strings"
	"time"
	"unicode/utf16"
)

// PIDPSIP carries the ATSC base PSIP tables (MGT, TVCT/CVCT, STT, RRT).
const PIDPSIP uint16 = 0x1ffb

// ---- ATSC Virtual Channel Table (A/65 6.3) ----

type VCTChannel struct {
	ShortName        string
	Major, Minor     int
	TSID             uint16
	Program          uint16
	ServiceType      byte // 0x02 digital TV, 0x03 audio, 0x04 data
	SourceID         uint16
	AccessControlled bool
	Hidden           bool
}

type VCT struct {
	TSID     uint16
	Channels []VCTChannel
}

// IsVCT reports whether tid is a terrestrial (0xC8) or cable (0xC9) VCT.
func IsVCT(tid byte) bool { return tid == 0xc8 || tid == 0xc9 }

// ParseVCT parses one TVCT or CVCT section.
func ParseVCT(s *Section) (*VCT, error) {
	if !IsVCT(s.TableID) {
		return nil, ErrBadSection
	}
	d := s.Data
	if len(d) < 2 {
		return nil, ErrBadSection
	}
	n, p := int(d[1]), 2 // protocol_version, num_channels_in_section
	v := &VCT{TSID: s.Ext}
	for i := 0; i < n; i++ {
		if p+32 > len(d) {
			return nil, ErrBadSection
		}
		c := d[p:]
		units := make([]uint16, 0, 7)
		for j := 0; j < 14; j += 2 {
			units = append(units, u16(c[j:]))
		}
		name := strings.TrimSpace(strings.TrimRight(string(utf16.Decode(units)), "\x00"))
		v.Channels = append(v.Channels, VCTChannel{
			ShortName:        name,
			Major:            int(c[14]&0x0f)<<6 | int(c[15]>>2),
			Minor:            int(c[15]&0x03)<<8 | int(c[16]),
			TSID:             u16(c[22:]),
			Program:          u16(c[24:]),
			AccessControlled: c[26]&0x20 != 0,
			Hidden:           c[26]&0x10 != 0,
			ServiceType:      c[27] & 0x3f,
			SourceID:         u16(c[28:]),
		})
		p += 32 + int(u16(c[30:])&0x3ff)
	}
	return v, nil
}

// BuildVCT assembles a TVCT section (used by tests).
func BuildVCT(tsid uint16, version byte, chans []VCTChannel) []byte {
	data := []byte{0, byte(len(chans))}
	for _, c := range chans {
		b := make([]byte, 32)
		for j, u := range utf16.Encode([]rune(c.ShortName)) {
			if j >= 7 {
				break
			}
			b[2*j], b[2*j+1] = byte(u>>8), byte(u)
		}
		b[14] = 0xf0 | byte(c.Major>>6)
		b[15] = byte(c.Major<<2) | byte(c.Minor>>8)
		b[16] = byte(c.Minor)
		b[17] = 0x04 // 8VSB
		b[22], b[23] = byte(c.TSID>>8), byte(c.TSID)
		b[24], b[25] = byte(c.Program>>8), byte(c.Program)
		b[26] = 0x0d
		if c.AccessControlled {
			b[26] |= 0x20
		}
		if c.Hidden {
			b[26] |= 0x10
		}
		b[27] = 0xc0 | c.ServiceType&0x3f
		b[28], b[29] = byte(c.SourceID>>8), byte(c.SourceID)
		b[30] = 0xfc
		data = append(data, b...)
	}
	data = append(data, 0xfc, 0x00) // additional_descriptors_length = 0
	return BuildSection(0xc8, tsid, version, 0, 0, data)
}

// ---- ATSC guide tables: MGT, STT, EIT, ETT (A/65) ----

const (
	TableMGT = 0xc7
	TableEIT = 0xcb // ATSC EIT (DVB EIT ids are 0x4e-0x6f)
	TableETT = 0xcc
	TableSTT = 0xcd
)

// DefaultGPSUTCOffset is the GPS-UTC leap second offset, used until an STT
// has been received.
const DefaultGPSUTCOffset = 18

var gpsEpoch = time.Date(1980, 1, 6, 0, 0, 0, 0, time.UTC)

// GPSTime converts ATSC GPS seconds to UTC.
func GPSTime(secs uint32, offset int) time.Time {
	return gpsEpoch.Add(time.Duration(int64(secs)-int64(offset)) * time.Second)
}

type MGTTable struct {
	Type    uint16 // 0x0100-0x017f EIT-0..127, 0x0200-0x027f event ETT-0..127
	PID     uint16
	Version byte
}

// IsGuide reports whether an MGT entry is an EIT or event ETT table.
func (t MGTTable) IsGuide() bool {
	return (t.Type >= 0x0100 && t.Type <= 0x017f) || (t.Type >= 0x0200 && t.Type <= 0x027f)
}

func ParseMGT(s *Section) ([]MGTTable, error) {
	d := s.Data
	if s.TableID != TableMGT || len(d) < 3 {
		return nil, ErrBadSection
	}
	n, p := int(u16(d[1:])), 3
	var out []MGTTable
	for i := 0; i < n; i++ {
		if p+11 > len(d) {
			return nil, ErrBadSection
		}
		out = append(out, MGTTable{Type: u16(d[p:]), PID: u13(d[p+2:]), Version: d[p+4] & 0x1f})
		p += 11 + u12(d[p+9:])
	}
	return out, nil
}

// ParseSTT returns the GPS-UTC offset carried in a System Time Table.
func ParseSTT(s *Section) (offset int, err error) {
	if s.TableID != TableSTT || len(s.Data) < 6 {
		return 0, ErrBadSection
	}
	return int(s.Data[5]), nil
}

type ATSCEvent struct {
	EventID  uint16
	Start    uint32 // GPS seconds
	Duration time.Duration
	Title    string
}

type ATSCEIT struct {
	SourceID uint16
	Events   []ATSCEvent
}

func ParseATSCEIT(s *Section) (*ATSCEIT, error) {
	d := s.Data
	if s.TableID != TableEIT || len(d) < 2 {
		return nil, ErrBadSection
	}
	e := &ATSCEIT{SourceID: s.Ext}
	n, p := int(d[1]), 2
	for i := 0; i < n; i++ {
		if p+10 > len(d) {
			return nil, ErrBadSection
		}
		c := d[p:]
		tl := int(c[9])
		if p+10+tl+2 > len(d) {
			return nil, ErrBadSection
		}
		e.Events = append(e.Events, ATSCEvent{
			EventID:  u16(c) & 0x3fff,
			Start:    uint32(c[2])<<24 | uint32(c[3])<<16 | uint32(c[4])<<8 | uint32(c[5]),
			Duration: time.Duration(int(c[6]&0x0f)<<16|int(c[7])<<8|int(c[8])) * time.Second,
			Title:    DecodeMSS(c[10 : 10+tl]),
		})
		p += 10 + tl
		p += 2 + u12(d[p:])
	}
	return e, nil
}

type ETT struct {
	SourceID uint16
	EventID  uint16
	Text     string
}

func ParseETT(s *Section) (*ETT, error) {
	d := s.Data
	if s.TableID != TableETT || len(d) < 5 {
		return nil, ErrBadSection
	}
	etm := uint32(d[1])<<24 | uint32(d[2])<<16 | uint32(d[3])<<8 | uint32(d[4])
	return &ETT{SourceID: uint16(etm >> 16), EventID: uint16(etm&0xffff) >> 2, Text: DecodeMSS(d[5:])}, nil
}

// DecodeMSS decodes an ATSC multiple_string_structure, preferring English.
// Huffman-compressed segments (rare in practice) are skipped.
func DecodeMSS(b []byte) string {
	if len(b) < 1 {
		return ""
	}
	n, p := int(b[0]), 1
	var first, eng string
	for i := 0; i < n; i++ {
		if p+4 > len(b) {
			break
		}
		lang, segs := string(b[p:p+3]), int(b[p+3])
		p += 4
		var sb strings.Builder
		for j := 0; j < segs; j++ {
			if p+3 > len(b) {
				break
			}
			comp, mode, nb := b[p], b[p+1], int(b[p+2])
			p += 3
			if p+nb > len(b) {
				break
			}
			seg := b[p : p+nb]
			p += nb
			if comp != 0 {
				continue
			}
			if mode == 0x3f {
				for k := 0; k+1 < len(seg); k += 2 {
					sb.WriteRune(rune(u16(seg[k:])))
				}
			} else if mode <= 0x33 {
				for _, c := range seg {
					sb.WriteRune(rune(int(mode)<<8 | int(c)))
				}
			}
		}
		text := strings.TrimSpace(sb.String())
		if first == "" {
			first = text
		}
		if lang == "eng" && eng == "" {
			eng = text
		}
	}
	if eng != "" {
		return eng
	}
	return first
}

// ---- builders (tests / tsinject) ----

func mss(text string) []byte {
	if text == "" {
		return []byte{0}
	}
	if len(text) > 255 {
		text = text[:255]
	}
	return append([]byte{1, 'e', 'n', 'g', 1, 0, 0, byte(len(text))}, text...)
}

func BuildMGT(tables []MGTTable) []byte {
	data := []byte{0, byte(len(tables) >> 8), byte(len(tables))}
	for _, t := range tables {
		data = append(data, byte(t.Type>>8), byte(t.Type), 0xe0|byte(t.PID>>8), byte(t.PID), 0xe0|t.Version, 0, 0, 0, 0, 0xf0, 0)
	}
	data = append(data, 0xf0, 0)
	return BuildSection(TableMGT, 0, 0, 0, 0, data)
}

func BuildSTT(now time.Time, offset int) []byte {
	secs := GPSSeconds(now, offset)
	return BuildSection(TableSTT, 0, 0, 0, 0, []byte{0, byte(secs >> 24), byte(secs >> 16), byte(secs >> 8), byte(secs), byte(offset), 0x60, 0})
}

func BuildATSCEIT(sourceID uint16, version byte, evs []ATSCEvent) []byte {
	data := []byte{0, byte(len(evs))}
	for _, e := range evs {
		t := mss(e.Title)
		d := uint32(e.Duration / time.Second)
		data = append(data, 0xc0|byte(e.EventID>>8), byte(e.EventID),
			byte(e.Start>>24), byte(e.Start>>16), byte(e.Start>>8), byte(e.Start),
			0xd0|byte(d>>16), byte(d>>8), byte(d), byte(len(t)))
		data = append(data, t...)
		data = append(data, 0xf0, 0)
	}
	return BuildSection(TableEIT, sourceID, version, 0, 0, data)
}

func BuildETT(sourceID, eventID uint16, version byte, text string) []byte {
	etm := uint32(sourceID)<<16 | uint32(eventID)<<2 | 2
	data := append([]byte{0, byte(etm >> 24), byte(etm >> 16), byte(etm >> 8), byte(etm)}, mss(text)...)
	return BuildSection(TableETT, uint16(etm>>16)^uint16(etm), version, 0, 0, data)
}

// GPSSeconds converts UTC to ATSC GPS seconds.
func GPSSeconds(t time.Time, offset int) uint32 {
	return uint32(t.Sub(gpsEpoch)/time.Second) + uint32(offset)
}
