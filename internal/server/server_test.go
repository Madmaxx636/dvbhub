package server

import (
	"encoding/binary"
	"hash/crc32"
	"strconv"
	"testing"

	"dvbhub/internal/config"
)

func TestCloneMergeKeepsUnsentFieldsAndDoesntAlias(t *testing.T) {
	old := &config.Channel{ID: "c", Name: "Old", Number: 4, Services: []string{"a", "b"}, Profile: "hd"}
	n, err := cloneMerge(old, []byte(`{"name":"New","services":["x"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "New" || n.Number != 4 || n.Profile != "hd" || len(n.Services) != 1 {
		t.Fatalf("merged %+v", n)
	}
	if old.Services[0] != "a" || old.Name != "Old" {
		t.Fatalf("the stored object was modified: %+v", old)
	}
	if _, err := cloneMerge(old, []byte(`{"number":"x"}`)); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestDeviceIDPerProfile(t *testing.T) {
	base := config.NewHDHRDeviceID()
	if deviceID(base, "original", "original") != base {
		t.Fatal("default profile must keep the base id")
	}
	id := deviceID(base, "hd", "original")
	if id == base || len(id) != 8 {
		t.Fatalf("profile id %s", id)
	}
	v, _ := strconv.ParseUint(id, 16, 32)
	if config.HDHRDeviceID(uint32(v)) != id {
		t.Fatalf("profile id %s has a bad checksum", id)
	}
}

func TestHDHRDiscoveryPacket(t *testing.T) {
	payload := tlv(tagBaseURL, []byte("http://10.0.0.2:9980"))
	p := hdhrPacket(hdhrDiscoverReply, payload)
	if binary.BigEndian.Uint16(p) != hdhrDiscoverReply || int(binary.BigEndian.Uint16(p[2:])) != len(payload) {
		t.Fatalf("header % x", p[:4])
	}
	if crc32.ChecksumIEEE(p[:len(p)-4]) != binary.LittleEndian.Uint32(p[len(p)-4:]) {
		t.Fatal("bad CRC")
	}
	long := tlv(tagLineupURL, make([]byte, 200))
	if long[1] != 200&0x7f|0x80 || long[2] != 1 {
		t.Fatalf("long TLV length % x", long[:3])
	}
}
