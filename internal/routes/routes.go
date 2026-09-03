// Package routes parses and normalizes route lists coming from the sources.
package routes

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Set is an ordered, de-duplicated collection of prefixes.
type Set struct {
	seen  map[netip.Prefix]struct{}
	items []netip.Prefix
}

// NewSet returns an empty Set.
func NewSet() *Set {
	return &Set{seen: make(map[netip.Prefix]struct{})}
}

// Add stores the prefix if it is not there yet and reports whether it was new.
func (s *Set) Add(p netip.Prefix) bool {
	if _, ok := s.seen[p]; ok {
		return false
	}
	s.seen[p] = struct{}{}
	s.items = append(s.items, p)
	return true
}

// AddAll stores every prefix of the slice.
func (s *Set) AddAll(ps []netip.Prefix) {
	for _, p := range ps {
		s.Add(p)
	}
}

// Len returns the number of unique prefixes.
func (s *Set) Len() int { return len(s.seen) }

// Sorted returns the prefixes ordered by family, address and mask length.
func (s *Set) Sorted() []netip.Prefix {
	out := make([]netip.Prefix, len(s.items))
	copy(out, s.items)
	sort.Slice(out, func(i, j int) bool { return Less(out[i], out[j]) })
	return out
}

// Less orders prefixes: IPv4 before IPv6, then by address, then by mask length.
func Less(a, b netip.Prefix) bool {
	if a.Addr().Is4() != b.Addr().Is4() {
		return a.Addr().Is4()
	}
	if c := a.Addr().Compare(b.Addr()); c != 0 {
		return c < 0
	}
	return a.Bits() < b.Bits()
}

// Strings renders prefixes in their canonical textual form.
func Strings(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

// ParseOne normalizes a single route token. A bare address is treated as a
// host route (/32 for IPv4, /128 for IPv6); host bits of a prefix are masked
// off so the result is always canonical.
func ParseOne(token string) (netip.Prefix, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return netip.Prefix{}, fmt.Errorf("empty route")
	}
	if strings.Contains(t, "/") {
		p, err := netip.ParsePrefix(t)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("parse prefix %q: %w", t, err)
		}
		return p.Masked(), nil
	}
	addr, err := netip.ParseAddr(t)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse address %q: %w", t, err)
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// Parse splits raw source content into prefixes. Routes may be separated by
// commas, semicolons, whitespace or newlines; blank lines and lines starting
// with '#' or '//' are ignored. All parse errors are collected so one bad
// entry does not hide the rest of the list.
func Parse(raw string) ([]netip.Prefix, error) {
	set := NewSet()
	var bad []string

	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		for _, token := range strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t'
		}) {
			p, err := ParseOne(token)
			if err != nil {
				bad = append(bad, token)
				continue
			}
			set.Add(p)
		}
	}

	if len(bad) > 0 {
		return set.Sorted(), fmt.Errorf("invalid routes: %s", strings.Join(bad, ", "))
	}
	return set.Sorted(), nil
}
