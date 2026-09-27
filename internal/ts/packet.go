// Package ts implements the parts of MPEG transport streams and DVB service
// information needed to discover services, filter them and read EPG data.
package ts

const (
	PacketSize = 188
	SyncByte   = 0x47

	PIDPAT  = 0x0000
	PIDCAT  = 0x0001
	PIDNIT  = 0x0010
	PIDSDT  = 0x0011
	PIDEIT  = 0x0012
	PIDTDT  = 0x0014
	PIDNull = 0x1fff
)

func PID(p []byte) uint16 { return uint16(p[1]&0x1f)<<8 | uint16(p[2]) }

func PUSI(p []byte) bool { return p[1]&0x40 != 0 }

func TEI(p []byte) bool { return p[1]&0x80 != 0 }

func HasPayload(p []byte) bool { return p[3]&0x10 != 0 }

func HasAdaptation(p []byte) bool { return p[3]&0x20 != 0 }

func CC(p []byte) byte { return p[3] & 0x0f }

func Scrambled(p []byte) bool { return p[3]&0xc0 != 0 }

// Payload returns the payload part of a packet, or nil if it has none.
func Payload(p []byte) []byte {
	if !HasPayload(p) {
		return nil
	}
	off := 4
	if HasAdaptation(p) {
		off += 1 + int(p[4])
	}
	if off >= PacketSize {
		return nil
	}
	return p[off:PacketSize]
}

// PCR returns the program clock reference in 27 MHz units if the packet carries one.
func PCR(p []byte) (uint64, bool) {
	if !HasAdaptation(p) || p[4] < 7 || p[5]&0x10 == 0 {
		return 0, false
	}
	base := uint64(p[6])<<25 | uint64(p[7])<<17 | uint64(p[8])<<9 | uint64(p[9])<<1 | uint64(p[10])>>7
	ext := uint64(p[10]&1)<<8 | uint64(p[11])
	return base*300 + ext, true
}

// Aligner turns an arbitrary byte stream into whole, sync-aligned packets.
type Aligner struct {
	buf []byte
}

// Feed appends data and returns all complete packets now available. The
// returned slice is freshly allocated and always a multiple of PacketSize.
func (a *Aligner) Feed(data []byte) []byte {
	a.buf = append(a.buf, data...)
	out := make([]byte, 0, len(a.buf))
	i := 0
	for i+PacketSize <= len(a.buf) {
		if a.buf[i] != SyncByte || (i+PacketSize < len(a.buf) && a.buf[i+PacketSize] != SyncByte) {
			i++
			continue
		}
		out = append(out, a.buf[i:i+PacketSize]...)
		i += PacketSize
	}
	a.buf = append(a.buf[:0], a.buf[i:]...)
	return out
}

// ForEach calls fn for each packet in an aligned buffer.
func ForEach(buf []byte, fn func(pkt []byte)) {
	for i := 0; i+PacketSize <= len(buf); i += PacketSize {
		fn(buf[i : i+PacketSize])
	}
}
