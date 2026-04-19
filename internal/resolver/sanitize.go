package resolver

import "net/netip"

// rejectedPrefixes is the v1 deny-list applied to every successfully
// parsed per-source IPv4 response. Addresses inside any of these prefixes
// are NEVER a legitimate public IP for a residential-edge machine, so
// seeing one from an echo service strongly suggests either a compromised
// service, a transparent-proxy-in-the-middle, or (most commonly) the
// service accidentally echoing a private-side NAT address.
//
// The ordering has two conventions:
//
//   - Most-specific first (e.g. link-local 169.254.0.0/16 before the
//     wider 169.254.* in case we ever split it) — only cosmetic; the
//     first match wins but Contains is commutative for non-overlapping
//     ranges.
//   - RFC 1918 first, then loopback, then the less-well-known ranges,
//     because the most common bug is "service returns the NAT-side
//     address", which is almost always RFC 1918.
//
// Adding a new prefix (Class E 240/4 when IETF eventually de-reserves it,
// for example) is a one-line change here — per issue 4.3 acceptance
// criterion.
var rejectedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),     // RFC 1918 private
	netip.MustParsePrefix("172.16.0.0/12"),  // RFC 1918 private
	netip.MustParsePrefix("192.168.0.0/16"), // RFC 1918 private
	netip.MustParsePrefix("127.0.0.0/8"),    // loopback
	netip.MustParsePrefix("169.254.0.0/16"), // link-local (DHCP failure)
	netip.MustParsePrefix("100.64.0.0/10"),  // CGNAT (RFC 6598)
	netip.MustParsePrefix("224.0.0.0/4"),    // multicast
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmark (RFC 2544)
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network" (RFC 1122)
}

// sanitize checks addr against rejectedPrefixes. Returns ok=true when the
// address is acceptable for quorum tallying; otherwise ok=false with a
// reason of the form "in_<prefix>" that callers surface via
// SourceResult.Error (namespaced with a "sanitize_reject:" prefix at the
// call site).
func sanitize(addr netip.Addr) (ok bool, reason string) {
	for _, p := range rejectedPrefixes {
		if p.Contains(addr) {
			return false, "in_" + p.String()
		}
	}
	return true, ""
}
