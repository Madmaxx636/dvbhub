package dvb

import "testing"

func TestParseScanFiles(t *testing.T) {
	v5 := `[CHANNEL]
	DELIVERY_SYSTEM = ATSC
	FREQUENCY = 57028615
	MODULATION = VSB/8

[CHANNEL]
	DELIVERY_SYSTEM = DVBS2
	FREQUENCY = 11727000
	POLARIZATION = HORIZONTAL
	SYMBOL_RATE = 27500000
	MODULATION = PSK/8
`
	ts, err := ParseScanFile(v5)
	if err != nil || len(ts) != 2 {
		t.Fatalf("%v %v", ts, err)
	}
	if ts[0].DeliverySystem != "ATSC" || ts[0].FrequencyKHz != 57028 || ts[0].Modulation != "8VSB" {
		t.Errorf("atsc %+v", ts[0])
	}
	if ts[1].DeliverySystem != "DVB-S2" || ts[1].FrequencyKHz != 11727000 || ts[1].Polarization != "H" || ts[1].Modulation != "8PSK" {
		t.Errorf("sat %+v", ts[1])
	}
	legacy, err := ParseScanFile("# comment\nT 506000000 8MHz 2/3 NONE QAM64 8k 1/32 NONE\nC 346000000 6900000 NONE QAM256\n")
	if err != nil || len(legacy) != 2 || legacy[0].BandwidthHz != 8000000 || legacy[1].SymbolRate != 6900000 {
		t.Fatalf("legacy %+v %v", legacy, err)
	}
}

func TestPlans(t *testing.T) {
	for _, p := range Plans() {
		if len(p.Channels) == 0 || DelsysForNetwork(p.Type) == nil {
			t.Errorf("plan %s is empty or has an unknown type", p.ID)
		}
	}
}
