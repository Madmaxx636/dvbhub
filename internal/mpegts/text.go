package mpegts

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// DecodeText decodes a DVB SI string (ETSI EN 300 468 Annex A) to UTF-8.
// Supported: ISO 6937 (default), ISO 8859-1/5/7/9/15, UCS-2 and UTF-8.
// Other 8859 parts fall back to Latin-1, which keeps ASCII intact.
func DecodeText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	table := "6937"
	switch c := b[0]; {
	case c >= 0x20:
	case c == 0x10 && len(b) >= 3:
		table = iso8859(int(b[2]))
		b = b[3:]
	case c == 0x11:
		return clean(decodeUCS2(b[1:]))
	case c == 0x15:
		return clean(strings.ToValidUTF8(string(b[1:]), "�"))
	case c == 0x1f && len(b) >= 2:
		return "" // encoding_type_id (e.g. Freesat Huffman) is not supported
	case c >= 0x01 && c <= 0x0b:
		table = iso8859(int(c) + 4)
		b = b[1:]
	default:
		b = b[1:]
		table = "8859-1"
	}
	var sb strings.Builder
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == 0x8a:
			sb.WriteByte('\n')
			continue
		case c >= 0x80 && c <= 0x9f:
			continue // emphasis and other control codes
		case c < 0x20:
			continue
		case c < 0x80:
			sb.WriteByte(c)
			continue
		}
		switch table {
		case "6937":
			if c >= 0xc1 && c <= 0xcf && i+1 < len(b) {
				sb.WriteByte(b[i+1])
				if m := iso6937Combining[c-0xc1]; m != 0 {
					sb.WriteRune(m)
				}
				i++
				continue
			}
			if r := iso6937High[c-0xa0]; r != 0 {
				sb.WriteRune(r)
			}
		case "8859-5":
			switch {
			case c == 0xa0:
				sb.WriteRune(0xa0)
			case c == 0xad:
				sb.WriteRune(0xad)
			case c == 0xf0:
				sb.WriteRune(0x2116)
			case c == 0xfd:
				sb.WriteRune(0xa7)
			default:
				sb.WriteRune(rune(c) + 0x0360)
			}
		case "8859-7":
			if c >= 0xb4 && c != 0xb7 && c != 0xbb && c != 0xbd {
				sb.WriteRune(rune(c) + 0x02d0)
			} else {
				sb.WriteRune(rune(c))
			}
		case "8859-9":
			sb.WriteRune(latin5(c))
		case "8859-15":
			sb.WriteRune(latin9(c))
		default:
			sb.WriteRune(rune(c))
		}
	}
	return clean(sb.String())
}

func iso8859(part int) string {
	switch part {
	case 5, 7, 9, 15:
		return "8859-" + strconv.Itoa(part)
	}
	return "8859-1"
}

func clean(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	return strings.TrimSpace(s)
}

// stripCharset removes a leading character-table selector.
func stripCharset(b []byte) []byte {
	switch {
	case len(b) == 0:
		return b
	case b[0] == 0x10 && len(b) >= 3:
		return b[3:]
	case b[0] < 0x20:
		return b[1:]
	}
	return b
}

func decodeUCS2(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		v := uint16(b[i])<<8 | uint16(b[i+1])
		if v == 0xe08a {
			v = '\n'
		} else if v >= 0xe080 && v <= 0xe09f {
			continue
		}
		u = append(u, v)
	}
	return string(utf16.Decode(u))
}

func latin5(c byte) rune {
	switch c {
	case 0xd0:
		return 'Ğ'
	case 0xdd:
		return 'İ'
	case 0xde:
		return 'Ş'
	case 0xf0:
		return 'ğ'
	case 0xfd:
		return 'ı'
	case 0xfe:
		return 'ş'
	}
	return rune(c)
}

func latin9(c byte) rune {
	switch c {
	case 0xa4:
		return '€'
	case 0xa6:
		return 'Š'
	case 0xa8:
		return 'š'
	case 0xb4:
		return 'Ž'
	case 0xb8:
		return 'ž'
	case 0xbc:
		return 'Œ'
	case 0xbd:
		return 'œ'
	case 0xbe:
		return 'Ÿ'
	}
	return rune(c)
}

// Combining diacritics for ISO 6937 0xC1..0xCF (non-spacing, precede the base letter).
var iso6937Combining = [15]rune{
	0x0300, 0x0301, 0x0302, 0x0303, 0x0304, 0x0306, 0x0307, 0x0308,
	0, 0x030a, 0x0327, 0, 0x030b, 0x0328, 0x030c,
}

// Spacing characters for ISO 6937 0xA0..0xFF (0 = undefined or handled elsewhere).
var iso6937High = [96]rune{
	0xa0, 0xa1, 0xa2, 0xa3, 0, 0xa5, 0, 0xa7, 0xa4, 0x2018, 0x201c, 0xab, 0x2190, 0x2191, 0x2192, 0x2193,
	0xb0, 0xb1, 0xb2, 0xb3, 0xd7, 0xb5, 0xb6, 0xb7, 0xf7, 0x2019, 0x201d, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0x2015, 0xb9, 0xae, 0xa9, 0x2122, 0x266a, 0xac, 0xa6, 0, 0, 0, 0, 0x215b, 0x215c, 0x215d, 0x215e,
	0x2126, 0xc6, 0xd0, 0xaa, 0x126, 0, 0x132, 0x13f, 0x141, 0xd8, 0x152, 0xba, 0xde, 0x166, 0x14a, 0x149,
	0x138, 0xe6, 0x111, 0xf0, 0x127, 0x131, 0x133, 0x140, 0x142, 0xf8, 0x153, 0xdf, 0xfe, 0x167, 0x14b, 0xad,
}
