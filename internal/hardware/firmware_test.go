package hardware

import "testing"

// The Hauppauge WinTV-dualHD's Si2157 driver first asks for an optional ROM
// patch, then loads the real firmware. Only a file that never loaded counts.
func TestNeededFirmware(t *testing.T) {
	loaded := []string{
		"[  124.589803] si2157 10-0062: found a 'Silicon Labs Si2157-A30 ROM 0x50'",
		"[  124.590100] si2157 10-0062: Direct firmware load for dvb_driver_si2157_rom50.fw failed with error -2",
		"[  124.590626] si2157 10-0062: downloading firmware from file 'dvb-tuner-si2157-a30-01.fw'",
		"[  128.012345] si2157 10-0062: firmware version: 3.0.5",
	}
	if got := neededFirmware(loaded); len(got) != 0 {
		t.Fatalf("firmware that loaded after an optional file was reported as needed: %v", got)
	}
	missing := []string{
		"[    9.1] si2157 9-0060: Direct firmware load for dvb_driver_si2157_rom50.fw failed with error -2",
		"[    9.2] si2157 9-0060: firmware file 'dvb-tuner-si2157-a30-01.fw' not found",
	}
	got := neededFirmware(missing)
	if !got["dvb-tuner-si2157-a30-01.fw"] {
		t.Fatalf("missing tuner firmware not reported: %v", got)
	}
	journal := []string{"si2157 9-0060: firmware file 'dvb-tuner-si2157-a30-01.fw' not found"}
	if !neededFirmware(journal)["dvb-tuner-si2157-a30-01.fw"] {
		t.Fatal("journalctl -o cat lines (no timestamp) not parsed")
	}
}

func TestInstallerActionsAreFixed(t *testing.T) {
	for _, a := range []string{"firmware", "replug", "tools", "tbs"} {
		if !validAction(a) {
			t.Errorf("%s should be allowed", a)
		}
	}
	for _, a := range []string{"", "rm -rf /", "firmware;reboot", "FIRMWARE"} {
		if validAction(a) {
			t.Errorf("%q must be rejected", a)
		}
	}
	for _, id := range []string{"../x", "20260101-120000;", ""} {
		if validJobID(id) {
			t.Errorf("job id %q must be rejected", id)
		}
	}
}
