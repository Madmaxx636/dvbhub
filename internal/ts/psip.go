package ts

import (
	"strings"
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
		b[30] = 0xfc
		data = append(data, b...)
	}
	data = append(data, 0xfc, 0x00) // additional_descriptors_length = 0
	return BuildSection(0xc8, tsid, version, 0, 0, data)
}
