package mpegts

import (
	"fmt"
	"time"
)

// Descriptor is a raw tag/length/value descriptor.
type Descriptor struct {
	Tag  byte
	Data []byte
}

func ParseDescriptors(b []byte) []Descriptor {
	var out []Descriptor
	for len(b) >= 2 {
		l := int(b[1])
		if 2+l > len(b) {
			break
		}
		out = append(out, Descriptor{Tag: b[0], Data: b[2 : 2+l]})
		b = b[2+l:]
	}
	return out
}

func u16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
func u12(b []byte) int    { return int(b[0]&0x0f)<<8 | int(b[1]) }
func u13(b []byte) uint16 { return uint16(b[0]&0x1f)<<8 | uint16(b[1]) }

// ---- PAT ----

type PAT struct {
	TSID     uint16
	Version  byte
	Programs map[uint16]uint16 // program_number -> PMT PID (0 is NIT)
}

func ParsePAT(s *Section) (*PAT, error) {
	if s.TableID != 0x00 {
		return nil, fmt.Errorf("not a PAT: table 0x%02x", s.TableID)
	}
	p := &PAT{TSID: s.Ext, Version: s.Version, Programs: map[uint16]uint16{}}
	for d := s.Data; len(d) >= 4; d = d[4:] {
		p.Programs[u16(d)] = u13(d[2:])
	}
	return p, nil
}

// BuildPAT returns a PAT section listing the given programs.
func BuildPAT(tsid uint16, version byte, programs map[uint16]uint16) []byte {
	var data []byte
	for prog, pid := range programs {
		data = append(data, byte(prog>>8), byte(prog), 0xe0|byte(pid>>8)&0x1f, byte(pid))
	}
	return BuildSection(0x00, tsid, version, 0, 0, data)
}

// ---- PMT ----

type ESInfo struct {
	StreamType byte
	PID        uint16
	Kind       string // e.g. H264, AAC, TELETEXT; "" if not a media stream
	Lang       string
	Descs      []Descriptor
}

type PMT struct {
	Program   uint16
	Version   byte
	PCRPID    uint16
	Scrambled bool
	Streams   []ESInfo
}

func ParsePMT(s *Section) (*PMT, error) {
	if s.TableID != 0x02 || len(s.Data) < 4 {
		return nil, fmt.Errorf("not a PMT")
	}
	p := &PMT{Program: s.Ext, Version: s.Version, PCRPID: u13(s.Data)}
	infoLen := u12(s.Data[2:])
	d := s.Data[4:]
	if infoLen > len(d) {
		return nil, ErrBadSection
	}
	for _, desc := range ParseDescriptors(d[:infoLen]) {
		if desc.Tag == 0x09 {
			p.Scrambled = true
		}
	}
	d = d[infoLen:]
	for len(d) >= 5 {
		es := ESInfo{StreamType: d[0], PID: u13(d[1:])}
		l := u12(d[3:])
		if 5+l > len(d) {
			break
		}
		es.Descs = ParseDescriptors(d[5 : 5+l])
		for _, desc := range es.Descs {
			if desc.Tag == 0x09 {
				p.Scrambled = true
			}
		}
		es.Kind, es.Lang = classify(es.StreamType, es.Descs)
		p.Streams = append(p.Streams, es)
		d = d[5+l:]
	}
	return p, nil
}

func classify(st byte, descs []Descriptor) (kind, lang string) {
	for _, d := range descs {
		switch {
		case d.Tag == 0x0a && len(d.Data) >= 3:
			lang = string(d.Data[:3])
		case (d.Tag == 0x56 || d.Tag == 0x59) && len(d.Data) >= 3 && lang == "":
			lang = string(d.Data[:3])
		}
	}
	switch st {
	case 0x01, 0x02:
		return "MPEG2VIDEO", lang
	case 0x1b:
		return "H264", lang
	case 0x24:
		return "HEVC", lang
	case 0x03, 0x04:
		return "MPEG2AUDIO", lang
	case 0x0f:
		return "AAC", lang
	case 0x11:
		return "AAC-LATM", lang
	case 0x81:
		return "AC3", lang
	case 0x87:
		return "EAC3", lang
	case 0x06:
		for _, d := range descs {
			switch d.Tag {
			case 0x6a:
				return "AC3", lang
			case 0x7a:
				return "EAC3", lang
			case 0x7c:
				return "AAC", lang
			case 0x56:
				return "TELETEXT", lang
			case 0x59:
				return "DVBSUB", lang
			case 0x7f:
				if len(d.Data) > 0 && d.Data[0] == 0x15 {
					return "AC4", lang
				}
			}
		}
	}
	return "", lang
}

// ---- SDT ----

type SDTService struct {
	SID      uint16
	EITSched bool
	EITPF    bool
	Running  byte
	FreeCA   bool
	Type     byte
	Provider string
	Name     string
}

type SDT struct {
	Actual   bool
	TSID     uint16
	ONID     uint16
	Services []SDTService
}

func ParseSDT(s *Section) (*SDT, error) {
	if (s.TableID != 0x42 && s.TableID != 0x46) || len(s.Data) < 3 {
		return nil, fmt.Errorf("not an SDT")
	}
	t := &SDT{Actual: s.TableID == 0x42, TSID: s.Ext, ONID: u16(s.Data)}
	d := s.Data[3:]
	for len(d) >= 5 {
		svc := SDTService{
			SID:      u16(d),
			EITSched: d[2]&0x02 != 0,
			EITPF:    d[2]&0x01 != 0,
			Running:  d[3] >> 5,
			FreeCA:   d[3]&0x10 != 0,
		}
		l := u12(d[3:])
		if 5+l > len(d) {
			break
		}
		for _, desc := range ParseDescriptors(d[5 : 5+l]) {
			if desc.Tag == 0x48 && len(desc.Data) >= 3 {
				svc.Type = desc.Data[0]
				pl := int(desc.Data[1])
				if 2+pl >= len(desc.Data) {
					continue
				}
				svc.Provider = DecodeText(desc.Data[2 : 2+pl])
				nl := int(desc.Data[2+pl])
				if 3+pl+nl <= len(desc.Data) {
					svc.Name = DecodeText(desc.Data[3+pl : 3+pl+nl])
				}
			}
		}
		t.Services = append(t.Services, svc)
		d = d[5+l:]
	}
	return t, nil
}

// ServiceKind classifies a DVB service_type.
func ServiceKind(t byte) string {
	switch t {
	case 0x01, 0x11, 0x16, 0x19, 0x1c, 0x1f, 0x20:
		return "tv"
	case 0x02, 0x07, 0x0a:
		return "radio"
	}
	return "other"
}

// ---- NIT ----

// Delivery holds tuning parameters found in a NIT delivery system descriptor.
type Delivery struct {
	System           string // DVB-T, DVB-T2, DVB-C, DVB-S, DVB-S2
	FrequencyKHz     uint32
	BandwidthHz      uint32
	SymbolRate       uint32
	Modulation       string
	FEC              string
	CodeRateLP       string
	GuardInterval    string
	TransmissionMode string
	Hierarchy        string
	Polarization     string
	Rolloff          string
	StreamID         int
	OrbitalPosition  string
}

type NITTransport struct {
	TSID     uint16
	ONID     uint16
	Delivery *Delivery
	LCN      map[uint16]int // service_id -> logical channel number
}

type NIT struct {
	Actual     bool
	NetworkID  uint16
	Name       string
	Transports []NITTransport
}

func ParseNIT(s *Section) (*NIT, error) {
	if (s.TableID != 0x40 && s.TableID != 0x41) || len(s.Data) < 2 {
		return nil, fmt.Errorf("not a NIT")
	}
	n := &NIT{Actual: s.TableID == 0x40, NetworkID: s.Ext}
	d := s.Data
	l := u12(d)
	d = d[2:]
	if l > len(d) {
		return nil, ErrBadSection
	}
	for _, desc := range ParseDescriptors(d[:l]) {
		if desc.Tag == 0x40 {
			n.Name = DecodeText(desc.Data)
		}
	}
	d = d[l:]
	if len(d) < 2 {
		return n, nil
	}
	l = u12(d)
	d = d[2:]
	if l < len(d) {
		d = d[:l]
	}
	for len(d) >= 6 {
		tr := NITTransport{TSID: u16(d), ONID: u16(d[2:])}
		dl := u12(d[4:])
		if 6+dl > len(d) {
			break
		}
		for _, desc := range ParseDescriptors(d[6 : 6+dl]) {
			parseNITDescriptor(&tr, desc)
		}
		n.Transports = append(n.Transports, tr)
		d = d[6+dl:]
	}
	return n, nil
}

func bcd(b []byte, digits int) uint32 {
	var v uint32
	for i := 0; i < digits; i++ {
		nib := b[i/2]
		if i%2 == 0 {
			nib >>= 4
		}
		v = v*10 + uint32(nib&0x0f)
	}
	return v
}

var (
	terrBandwidth = []uint32{8e6, 7e6, 6e6, 5e6}
	terrConst     = []string{"QPSK", "QAM/16", "QAM/64", "AUTO"}
	terrHier      = []string{"NONE", "1", "2", "4", "NONE", "1", "2", "4"}
	terrRate      = []string{"1/2", "2/3", "3/4", "5/6", "7/8", "AUTO", "AUTO", "AUTO"}
	terrGuard     = []string{"1/32", "1/16", "1/8", "1/4"}
	terrMode      = []string{"2K", "8K", "4K", "AUTO"}
	t2Bandwidth   = []uint32{8e6, 7e6, 6e6, 5e6, 10e6, 1712e3}
	t2Guard       = []string{"1/32", "1/16", "1/8", "1/4", "1/128", "19/128", "19/256", "AUTO"}
	t2Mode        = []string{"2K", "8K", "4K", "1K", "16K", "32K", "AUTO", "AUTO"}
	cableMod      = []string{"AUTO", "QAM/16", "QAM/32", "QAM/64", "QAM/128", "QAM/256"}
	innerFEC      = []string{"AUTO", "1/2", "2/3", "3/4", "5/6", "7/8", "8/9", "3/5", "4/5", "9/10", "AUTO", "AUTO", "AUTO", "AUTO", "AUTO", "NONE"}
	satPol        = []string{"H", "V", "L", "R"}
	satRolloff    = []string{"35", "25", "20", "AUTO"}
	satMod        = []string{"AUTO", "QPSK", "8PSK", "QAM/16"}
)

func pick(tab []string, i int) string {
	if i < len(tab) {
		return tab[i]
	}
	return "AUTO"
}

func parseNITDescriptor(tr *NITTransport, desc Descriptor) {
	d := desc.Data
	switch desc.Tag {
	case 0x5a: // terrestrial
		if len(d) < 7 {
			return
		}
		f := uint32(uint64(d[0])<<24|uint64(d[1])<<16|uint64(d[2])<<8|uint64(d[3])) / 100 // 10 Hz units -> kHz
		bw := d[4] >> 5
		del := &Delivery{System: "DVB-T", FrequencyKHz: f, StreamID: -1,
			Modulation:       pick(terrConst, int(d[5]>>6)),
			Hierarchy:        pick(terrHier, int(d[5]>>3&7)),
			FEC:              pick(terrRate, int(d[5]&7)),
			CodeRateLP:       pick(terrRate, int(d[6]>>5)),
			GuardInterval:    pick(terrGuard, int(d[6]>>3&3)),
			TransmissionMode: pick(terrMode, int(d[6]>>1&3)),
		}
		if int(bw) < len(terrBandwidth) {
			del.BandwidthHz = terrBandwidth[bw]
		}
		if tr.Delivery == nil || tr.Delivery.System != "DVB-T2" {
			tr.Delivery = del
		}
	case 0x7f: // extension
		if len(d) < 4 || d[0] != 0x04 { // T2 delivery system
			return
		}
		del := &Delivery{System: "DVB-T2", StreamID: int(d[1]), Modulation: "AUTO", FEC: "AUTO",
			GuardInterval: "AUTO", TransmissionMode: "AUTO", Hierarchy: "NONE", CodeRateLP: "AUTO"}
		if tr.Delivery != nil && tr.Delivery.System == "DVB-T" {
			del.FrequencyKHz = tr.Delivery.FrequencyKHz
			del.BandwidthHz = tr.Delivery.BandwidthHz
		}
		if len(d) >= 6 {
			bw := int(d[4] >> 2 & 0x0f)
			if bw < len(t2Bandwidth) {
				del.BandwidthHz = t2Bandwidth[bw]
			}
			del.GuardInterval = pick(t2Guard, int(d[5]>>5))
			del.TransmissionMode = pick(t2Mode, int(d[5]>>2&7))
			tfs := d[5]&1 != 0
			cells := d[6:]
			if len(cells) >= 2 {
				cells = cells[2:] // cell_id
				if tfs {
					if len(cells) >= 5 {
						cells = cells[1:]
						del.FrequencyKHz = uint32(uint64(u16(cells))<<16|uint64(u16(cells[2:]))) / 100
					}
				} else if len(cells) >= 4 {
					del.FrequencyKHz = uint32(uint64(u16(cells))<<16|uint64(u16(cells[2:]))) / 100
				}
			}
		}
		if del.FrequencyKHz != 0 {
			tr.Delivery = del
		}
	case 0x44: // cable
		if len(d) < 11 {
			return
		}
		tr.Delivery = &Delivery{System: "DVB-C", StreamID: -1,
			FrequencyKHz: bcd(d, 8) / 10, // XXXX.XXXX MHz -> kHz
			Modulation:   pick(cableMod, int(d[6])),
			SymbolRate:   bcd(d[7:], 7) * 100, // XXX.XXXX Msym/s
			FEC:          innerFEC[d[10]&0x0f],
		}
	case 0x43: // satellite
		if len(d) < 11 {
			return
		}
		sys := "DVB-S"
		if d[6]&0x04 != 0 {
			sys = "DVB-S2"
		}
		orb := bcd(d[4:], 4)
		dir := "E"
		if d[6]&0x80 == 0 {
			dir = "W"
		}
		tr.Delivery = &Delivery{System: sys, StreamID: -1,
			FrequencyKHz:    bcd(d, 8) * 10, // XXX.XXXXX GHz -> kHz
			OrbitalPosition: fmt.Sprintf("%d.%d%s", orb/10, orb%10, dir),
			Polarization:    satPol[d[6]>>5&3],
			Rolloff:         satRolloff[d[6]>>3&3],
			Modulation:      satMod[d[6]&3],
			SymbolRate:      bcd(d[7:], 7) * 100,
			FEC:             innerFEC[d[10]&0x0f],
		}
		if sys == "DVB-S" {
			tr.Delivery.Rolloff = "35"
		}
	case 0x83: // logical channel (EACEM / NorDig v1)
		for ; len(d) >= 4; d = d[4:] {
			if tr.LCN == nil {
				tr.LCN = map[uint16]int{}
			}
			tr.LCN[u16(d)] = int(u16(d[2:]) & 0x3ff)
		}
	}
}

// ---- EIT ----

type Event struct {
	EventID     uint16
	Start       time.Time
	Duration    time.Duration
	Running     byte
	Title       string
	Subtitle    string // short event "text" is usually a synopsis; kept separately
	Description string
	Lang        string
	Genres      []byte // content nibble pairs (level1<<4 | level2)
	Rating      int    // minimum age, 0 if unknown
}

type EIT struct {
	TableID byte
	SID     uint16
	TSID    uint16
	ONID    uint16
	Version byte
	Events  []Event
}

var mjdEpoch = time.Date(1858, 11, 17, 0, 0, 0, 0, time.UTC)

func decodeTime(b []byte) time.Time {
	mjd := int(u16(b))
	if mjd == 0xffff {
		return time.Time{}
	}
	h, m, s := bcd(b[2:], 2), bcd(b[3:], 2), bcd(b[4:], 2)
	return mjdEpoch.AddDate(0, 0, mjd).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
}

func decodeDuration(b []byte) time.Duration {
	return time.Duration(bcd(b, 2))*time.Hour + time.Duration(bcd(b[1:], 2))*time.Minute + time.Duration(bcd(b[2:], 2))*time.Second
}

// IsEIT reports whether a table id belongs to the EIT family.
func IsEIT(tid byte) bool { return tid >= 0x4e && tid <= 0x6f }

func ParseEIT(s *Section) (*EIT, error) {
	if !IsEIT(s.TableID) || len(s.Data) < 6 {
		return nil, fmt.Errorf("not an EIT")
	}
	e := &EIT{TableID: s.TableID, SID: s.Ext, TSID: u16(s.Data), ONID: u16(s.Data[2:]), Version: s.Version}
	d := s.Data[6:]
	for len(d) >= 12 {
		ev := Event{
			EventID:  u16(d),
			Start:    decodeTime(d[2:7]),
			Duration: decodeDuration(d[7:10]),
			Running:  d[10] >> 5,
		}
		l := u12(d[10:])
		if 12+l > len(d) {
			break
		}
		ext := map[int]string{}
		maxExt := -1
		for _, desc := range ParseDescriptors(d[12 : 12+l]) {
			dd := desc.Data
			switch desc.Tag {
			case 0x4d: // short event
				if len(dd) < 5 {
					continue
				}
				ev.Lang = string(dd[:3])
				nl := int(dd[3])
				if 4+nl >= len(dd) {
					continue
				}
				ev.Title = DecodeText(dd[4 : 4+nl])
				tl := int(dd[4+nl])
				if 5+nl+tl <= len(dd) {
					ev.Subtitle = DecodeText(dd[5+nl : 5+nl+tl])
				}
			case 0x4e: // extended event
				if len(dd) < 6 {
					continue
				}
				num := int(dd[0] >> 4)
				il := int(dd[4])
				if 5+il >= len(dd) {
					continue
				}
				tl := int(dd[5+il])
				if 6+il+tl <= len(dd) {
					ext[num] += string(dd[6+il : 6+il+tl]) // decoded once concatenated
					if num > maxExt {
						maxExt = num
					}
				}
			case 0x54: // content
				for i := 0; i+1 < len(dd); i += 2 {
					ev.Genres = append(ev.Genres, dd[i])
				}
			case 0x55: // parental rating
				if len(dd) >= 4 && dd[3] >= 1 && dd[3] <= 0x0f {
					ev.Rating = int(dd[3]) + 3
				}
			}
		}
		if maxExt >= 0 {
			var raw []byte
			for i := 0; i <= maxExt; i++ {
				chunk := []byte(ext[i])
				// Each chunk may repeat the charset selector; keep only the first.
				if i > 0 && len(chunk) > 0 && chunk[0] < 0x20 {
					chunk = stripCharset(chunk)
				}
				raw = append(raw, chunk...)
			}
			ev.Description = DecodeText(raw)
		}
		e.Events = append(e.Events, ev)
		d = d[12+l:]
	}
	return e, nil
}

// GenreName maps a DVB content nibble to an XMLTV-style category.
func GenreName(g byte) string {
	switch g >> 4 {
	case 0x1:
		return "Movie / Drama"
	case 0x2:
		return "News / Current affairs"
	case 0x3:
		return "Show / Game show"
	case 0x4:
		return "Sports"
	case 0x5:
		return "Children's / Youth programmes"
	case 0x6:
		return "Music / Ballet / Dance"
	case 0x7:
		return "Arts / Culture"
	case 0x8:
		return "Social / Political issues / Economics"
	case 0x9:
		return "Education / Science / Factual topics"
	case 0xa:
		return "Leisure hobbies"
	}
	return ""
}

// ---- builders (used by tests and the stream generator tool) ----

func encodeMJD(t time.Time) []byte {
	t = t.UTC()
	mjd := int(t.Sub(mjdEpoch).Hours() / 24)
	h, m, s := t.Hour(), t.Minute(), t.Second()
	return []byte{byte(mjd >> 8), byte(mjd), toBCD(h), toBCD(m), toBCD(s)}
}

func toBCD(v int) byte { return byte(v/10<<4 | v%10) }

func desc(tag byte, data []byte) []byte { return append([]byte{tag, byte(len(data))}, data...) }

// BuildSDT builds an SDT-actual section.
func BuildSDT(tsid, onid uint16, version byte, svcs []SDTService) []byte {
	data := []byte{byte(onid >> 8), byte(onid), 0xff}
	for _, s := range svcs {
		sd := []byte{s.Type, byte(len(s.Provider))}
		sd = append(sd, s.Provider...)
		sd = append(sd, byte(len(s.Name)))
		sd = append(sd, s.Name...)
		ds := desc(0x48, sd)
		flags := byte(0xfc)
		if s.EITSched {
			flags |= 2
		}
		if s.EITPF {
			flags |= 1
		}
		data = append(data, byte(s.SID>>8), byte(s.SID), flags, 0x80|byte(len(ds)>>8)&0x0f, byte(len(ds)))
		data = append(data, ds...)
	}
	return BuildSection(0x42, tsid, version, 0, 0, data)
}

// BuildNIT builds a NIT-actual section with a network name, terrestrial
// delivery descriptors and LCNs.
func BuildNIT(nid uint16, version byte, name string, transports []NITTransport) []byte {
	nd := desc(0x40, []byte(name))
	data := []byte{0xf0 | byte(len(nd)>>8), byte(len(nd))}
	data = append(data, nd...)
	var loop []byte
	for _, tr := range transports {
		var ds []byte
		if del := tr.Delivery; del != nil {
			f := del.FrequencyKHz * 100
			ds = append(ds, desc(0x5a, []byte{byte(f >> 24), byte(f >> 16), byte(f >> 8), byte(f), 0x1f, 0x82, 0x0a, 0xff, 0xff, 0xff, 0xff})...)
		}
		if len(tr.LCN) > 0 {
			var l []byte
			for sid, n := range tr.LCN {
				l = append(l, byte(sid>>8), byte(sid), 0xfc|byte(n>>8)&3, byte(n))
			}
			ds = append(ds, desc(0x83, l)...)
		}
		loop = append(loop, byte(tr.TSID>>8), byte(tr.TSID), byte(tr.ONID>>8), byte(tr.ONID), 0xf0|byte(len(ds)>>8), byte(len(ds)))
		loop = append(loop, ds...)
	}
	data = append(data, 0xf0|byte(len(loop)>>8), byte(len(loop)))
	data = append(data, loop...)
	return BuildSection(0x40, nid, version, 0, 0, data)
}

// BuildEIT builds one EIT section (table 0x4e for p/f, 0x50.. for schedule).
func BuildEIT(tableID byte, sid, tsid, onid uint16, version, number, last byte, events []Event) []byte {
	data := []byte{byte(tsid >> 8), byte(tsid), byte(onid >> 8), byte(onid), last, tableID}
	for _, ev := range events {
		var ds []byte
		se := []byte("eng")
		se = append(se, byte(len(ev.Title)))
		se = append(se, ev.Title...)
		se = append(se, byte(len(ev.Subtitle)))
		se = append(se, ev.Subtitle...)
		ds = append(ds, desc(0x4d, se)...)
		if ev.Description != "" {
			txt := ev.Description
			if len(txt) > 240 {
				txt = txt[:240]
			}
			ee := []byte{0x00, 'e', 'n', 'g', 0, byte(len(txt))}
			ee = append(ee, txt...)
			ds = append(ds, desc(0x4e, ee)...)
		}
		if len(ev.Genres) > 0 {
			var c []byte
			for _, g := range ev.Genres {
				c = append(c, g, 0)
			}
			ds = append(ds, desc(0x54, c)...)
		}
		dur := int(ev.Duration.Seconds())
		data = append(data, byte(ev.EventID>>8), byte(ev.EventID))
		data = append(data, encodeMJD(ev.Start)...)
		data = append(data, toBCD(dur/3600), toBCD(dur/60%60), toBCD(dur%60))
		data = append(data, ev.Running<<5|byte(len(ds)>>8)&0x0f, byte(len(ds)))
		data = append(data, ds...)
	}
	return BuildSection(tableID, sid, version, number, last, data)
}
