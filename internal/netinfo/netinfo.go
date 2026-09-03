// Package netinfo detects the primary network interface of the host.
package netinfo

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Primary describes the interface the default route points to.
type Primary struct {
	Interface string
	IP        netip.Addr
	MAC       string
}

// String renders the primary interface for logs.
func (p Primary) String() string {
	return fmt.Sprintf("%s (%s)", p.IP, p.Interface)
}

// probeTarget is only used to ask the kernel which local address would be
// picked for an off-link destination; no packet is ever sent (UDP "connect"
// is a routing table lookup).
const probeTarget = "1.1.1.1:53"

// Detect resolves the primary interface and its IPv4 address.
//
// ifaceOverride and ipOverride come from the configuration: either one, both
// or neither may be set. When both are set no detection happens at all, which
// makes the service usable on hosts with an unusual routing setup.
func Detect(ifaceOverride, ipOverride string) (Primary, error) {
	ifaceOverride = strings.TrimSpace(ifaceOverride)
	ipOverride = strings.TrimSpace(ipOverride)

	if ipOverride != "" {
		addr, err := netip.ParseAddr(ipOverride)
		if err != nil {
			return Primary{}, fmt.Errorf("general.ip-address: %w", err)
		}
		addr = addr.Unmap()
		name := ifaceOverride
		mac := ""
		if name == "" {
			if iface, err := interfaceFor(addr); err == nil {
				name, mac = iface.Name, iface.HardwareAddr.String()
			}
		} else if iface, err := net.InterfaceByName(name); err == nil {
			mac = iface.HardwareAddr.String()
		}
		return Primary{Interface: name, IP: addr, MAC: mac}, nil
	}

	if ifaceOverride != "" {
		iface, err := net.InterfaceByName(ifaceOverride)
		if err != nil {
			return Primary{}, fmt.Errorf("general.interface: %w", err)
		}
		addr, err := firstIPv4(iface)
		if err != nil {
			return Primary{}, err
		}
		return Primary{Interface: iface.Name, IP: addr, MAC: iface.HardwareAddr.String()}, nil
	}

	addr, err := outboundAddr()
	if err != nil {
		return Primary{}, err
	}
	iface, err := interfaceFor(addr)
	if err != nil {
		return Primary{}, err
	}
	return Primary{Interface: iface.Name, IP: addr, MAC: iface.HardwareAddr.String()}, nil
}

// outboundAddr returns the local address the default route would use.
func outboundAddr() (netip.Addr, error) {
	conn, err := net.Dial("udp4", probeTarget)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("detect default route: %w", err)
	}
	defer conn.Close()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, fmt.Errorf("detect default route: unexpected local address %v", conn.LocalAddr())
	}
	addr, ok := netip.AddrFromSlice(local.IP)
	if !ok {
		return netip.Addr{}, fmt.Errorf("detect default route: bad local address %v", local.IP)
	}
	return addr.Unmap(), nil
}

// interfaceFor finds the interface that owns the given address.
func interfaceFor(addr netip.Addr) (*net.Interface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	for i := range ifaces {
		iface := ifaces[i]
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			cur, ok := netip.AddrFromSlice(ipnet.IP)
			if ok && cur.Unmap() == addr {
				return &iface, nil
			}
		}
	}
	return nil, fmt.Errorf("no interface owns address %s", addr)
}

// firstIPv4 returns the first usable IPv4 address of an interface.
func firstIPv4(iface *net.Interface) (netip.Addr, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("addresses of %s: %w", iface.Name, err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipnet.IP)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if addr.Is4() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() {
			return addr, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("interface %s has no usable IPv4 address", iface.Name)
}
