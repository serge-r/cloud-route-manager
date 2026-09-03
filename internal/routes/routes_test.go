package routes

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestParseOne(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{in: "1.1.1.1", want: "1.1.1.1/32"},
		{in: " 10.0.0.0/8 ", want: "10.0.0.0/8"},
		{in: "10.1.2.3/24", want: "10.1.2.0/24"},
		{in: "2001:db8::1", want: "2001:db8::1/128"},
		{in: "2001:db8::1/32", want: "2001:db8::/32"},
		{in: "", err: true},
		{in: "not-an-ip", err: true},
		{in: "1.1.1.1/64", err: true},
	}
	for _, c := range cases {
		got, err := ParseOne(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseOne(%q) = %v, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseOne(%q): unexpected error %v", c.in, err)
			continue
		}
		if got.String() != c.want {
			t.Errorf("ParseOne(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	raw := "1.1.1.1, 2.2.2.2\n10.0.0.0/8;1.1.1.1\n# comment 9.9.9.9\n\n  \t\n192.168.0.1 // trailing\n"
	got, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"1.1.1.1/32", "2.2.2.2/32", "10.0.0.0/8", "192.168.0.1/32"}
	if !reflect.DeepEqual(Strings(got), want) {
		t.Fatalf("Parse = %v, want %v", Strings(got), want)
	}
}

func TestParseReportsBadEntriesAndKeepsGoodOnes(t *testing.T) {
	got, err := Parse("1.1.1.1\nnope\n2.2.2.2")
	if err == nil {
		t.Fatal("Parse: expected an error for the bad entry")
	}
	if want := []string{"1.1.1.1/32", "2.2.2.2/32"}; !reflect.DeepEqual(Strings(got), want) {
		t.Fatalf("Parse = %v, want %v", Strings(got), want)
	}
}

func TestSetDedupAndSort(t *testing.T) {
	s := NewSet()
	for _, raw := range []string{"10.0.0.0/8", "1.1.1.1/32", "10.0.0.0/8", "2001:db8::/32", "1.1.1.0/24"} {
		p := netip.MustParsePrefix(raw)
		s.Add(p)
	}
	if s.Len() != 4 {
		t.Fatalf("Len = %d, want 4", s.Len())
	}
	want := []string{"1.1.1.0/24", "1.1.1.1/32", "10.0.0.0/8", "2001:db8::/32"}
	if !reflect.DeepEqual(Strings(s.Sorted()), want) {
		t.Fatalf("Sorted = %v, want %v", Strings(s.Sorted()), want)
	}
}
