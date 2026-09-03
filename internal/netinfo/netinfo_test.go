package netinfo

import "testing"

func TestDetectHonoursTheAddressOverride(t *testing.T) {
	got, err := Detect("", "10.11.12.13")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got.IP.String() != "10.11.12.13" {
		t.Errorf("IP = %s, want 10.11.12.13", got.IP)
	}
}

func TestDetectRejectsABadAddressOverride(t *testing.T) {
	if _, err := Detect("", "not-an-ip"); err == nil {
		t.Fatal("Detect succeeded with an invalid ip-address override")
	}
}

func TestDetectRejectsAnUnknownInterface(t *testing.T) {
	if _, err := Detect("definitely-not-an-interface", ""); err == nil {
		t.Fatal("Detect succeeded with an unknown interface override")
	}
}

func TestDetectFindsThePrimaryInterface(t *testing.T) {
	got, err := Detect("", "")
	if err != nil {
		t.Skipf("no default route on this host: %v", err)
	}
	if !got.IP.IsValid() || got.Interface == "" {
		t.Fatalf("Detect = %+v, want an interface and an address", got)
	}
}
