// Package ssrf stops outgoing requests from reaching internal networks.
//
// The check runs in the dialer, on the address actually being connected to,
// so a hostname that resolves to a public address when validated and to an
// internal one when dialed (DNS rebinding) is still caught.
package ssrf

import (
	"errors"
	"fmt"
	"net/netip"
	"syscall"
)

// ErrBlocked is returned when a connection to a non-public address is refused.
var ErrBlocked = errors.New("destination is not a public address")

// Ranges that the netip predicates don't cover.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, including broadcast
	netip.MustParsePrefix("::/96"),           // IPv4-compatible (deprecated)
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard
	netip.MustParsePrefix("2001::/32"),       // Teredo: embeds an obfuscated IPv4 address
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("fec0::/10"),       // site-local (deprecated)
}

var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// Allowed reports whether ip is a public unicast address.
func Allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	// Translation prefixes reach whatever IPv4 address they embed.
	b := ip.As16()
	switch {
	case nat64.Contains(ip):
		return Allowed(netip.AddrFrom4([4]byte(b[12:16])))
	case sixToFour.Contains(ip):
		return Allowed(netip.AddrFrom4([4]byte(b[2:6])))
	}
	return true
}

// Control is a net.Dialer Control function that refuses connections to
// non-public addresses with an error wrapping ErrBlocked.
func Control(network, address string, _ syscall.RawConn) error {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return fmt.Errorf("%w: network %q", ErrBlocked, network)
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBlocked, err)
	}
	if !Allowed(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlocked, ap.Addr())
	}
	return nil
}
