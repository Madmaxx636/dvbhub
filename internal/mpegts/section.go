package mpegts

import (
	"errors"
)

var crcTable [256]uint32

func init() {
	for i := range crcTable {
		c := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		crcTable[i] = c
	}
}

// CRC32 computes the MPEG-2 CRC used by PSI/SI sections.
func CRC32(b []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, v := range b {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^v]
	}
	return crc
}

// SectionAssembler reassembles PSI/SI sections carried on a single PID.
type SectionAssembler struct {
	buf     []byte
	started bool
	lastCC  int
}

func NewSectionAssembler() *SectionAssembler { return &SectionAssembler{lastCC: -1} }

// Push feeds one TS packet and calls fn for every section completed by it.
func (a *SectionAssembler) Push(pkt []byte, fn func(section []byte)) {
	if TEI(pkt) {
		return
	}
	payload := Payload(pkt)
	if payload == nil {
		return
	}
	cc := int(CC(pkt))
	if a.lastCC >= 0 && cc == a.lastCC {
		return // duplicate packet
	}
	lost := a.lastCC >= 0 && cc != (a.lastCC+1)&0x0f
	a.lastCC = cc

	if PUSI(pkt) {
		ptr := int(payload[0])
		payload = payload[1:]
		if ptr > len(payload) {
			a.reset()
			return
		}
		if a.started && !lost {
			a.buf = append(a.buf, payload[:ptr]...)
			a.flush(fn)
		}
		a.buf = a.buf[:0]
		a.started = true
		a.buf = append(a.buf, payload[ptr:]...)
		a.flush(fn)
		return
	}
	if !a.started {
		return
	}
	if lost {
		a.reset()
		return
	}
	a.buf = append(a.buf, payload...)
	a.flush(fn)
}

func (a *SectionAssembler) reset() {
	a.buf = a.buf[:0]
	a.started = false
}

func (a *SectionAssembler) flush(fn func([]byte)) {
	for {
		if len(a.buf) == 0 {
			return
		}
		if a.buf[0] == 0xff { // stuffing: rest of the packet is padding
			a.reset()
			return
		}
		if len(a.buf) < 3 {
			return
		}
		total := 3 + (int(a.buf[1]&0x0f)<<8 | int(a.buf[2]))
		if len(a.buf) < total {
			return
		}
		sec := make([]byte, total)
		copy(sec, a.buf[:total])
		a.buf = a.buf[:copy(a.buf, a.buf[total:])]
		fn(sec)
	}
}

// Section is a parsed long-form (or short-form) PSI/SI section header.
type Section struct {
	TableID byte
	Syntax  bool
	Ext     uint16 // table_id_extension
	Version byte
	Current bool
	Number  byte
	Last    byte
	Data    []byte // bytes after the header, excluding CRC
}

var ErrBadSection = errors.New("ts: malformed section")
var ErrCRC = errors.New("ts: section CRC mismatch")

func ParseSection(b []byte) (*Section, error) {
	if len(b) < 3 {
		return nil, ErrBadSection
	}
	s := &Section{TableID: b[0], Syntax: b[1]&0x80 != 0}
	total := 3 + (int(b[1]&0x0f)<<8 | int(b[2]))
	if total > len(b) {
		return nil, ErrBadSection
	}
	b = b[:total]
	if !s.Syntax {
		s.Data = b[3:]
		return s, nil
	}
	if len(b) < 12 {
		return nil, ErrBadSection
	}
	if CRC32(b) != 0 {
		return nil, ErrCRC
	}
	s.Ext = uint16(b[3])<<8 | uint16(b[4])
	s.Version = (b[5] >> 1) & 0x1f
	s.Current = b[5]&1 != 0
	s.Number = b[6]
	s.Last = b[7]
	s.Data = b[8 : len(b)-4]
	return s, nil
}

// BuildSection assembles a long-form section with CRC.
func BuildSection(tableID byte, ext uint16, version byte, number, last byte, data []byte) []byte {
	length := 5 + len(data) + 4
	b := make([]byte, 0, 3+length)
	b = append(b, tableID, 0xb0|byte(length>>8)&0x0f, byte(length))
	b = append(b, byte(ext>>8), byte(ext), 0xc1|(version&0x1f)<<1, number, last)
	b = append(b, data...)
	crc := CRC32(b)
	return append(b, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// Packetize splits a section into TS packets on pid, advancing *cc.
func Packetize(pid uint16, section []byte, cc *byte) []byte {
	var out []byte
	first := true
	for first || len(section) > 0 {
		pkt := make([]byte, PacketSize)
		pkt[0] = SyncByte
		pkt[1] = byte(pid>>8) & 0x1f
		pkt[2] = byte(pid)
		pkt[3] = 0x10 | *cc&0x0f
		*cc = (*cc + 1) & 0x0f
		off := 4
		if first {
			pkt[1] |= 0x40
			pkt[4] = 0 // pointer field
			off = 5
			first = false
		}
		n := copy(pkt[off:], section)
		section = section[n:]
		for i := off + n; i < PacketSize; i++ {
			pkt[i] = 0xff
		}
		out = append(out, pkt...)
	}
	return out
}
