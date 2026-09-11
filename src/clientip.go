package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ---------------------------------------------------------------------------
// Who is calling
//
// Behind a reverse proxy, r.RemoteAddr is the proxy. That is not a cosmetic
// problem. The login and registration limiters are keyed by address, so with
// one proxy in front of them every caller in the world shares a single bucket:
// the brake that is supposed to slow one attacker down instead throttles
// everyone at once, and the "ip" field in the log names a container on the
// Docker bridge. Both were true of this server until the header below was
// consulted.
//
// The rule is deliberately conservative. A forwarded header is a claim made by
// whoever sent the request, so it is believed only when the immediate peer is
// an address the operator has named as a proxy. With trusted_proxies unset -
// the default - nothing is believed and the behaviour is exactly what it was
// before: RemoteAddr, always.
//
// Two shapes of header exist and both are handled by the same walk. A
// multi-valued X-Forwarded-For is read right to left, discarding hops that are
// themselves trusted, and the first address that is not a trusted proxy is the
// client: entries further left were appended by machines we do not vouch for
// and can be forged. A single-valued header such as CF-Connecting-IP is that
// walk with one element, which is why the same code serves both.
//
// Which to pick depends on what is actually in front of the server. With only
// a local reverse proxy, X-Forwarded-For is right. With a CDN in front of that
// proxy, the CDN's own header is right, because the proxy appends the CDN edge
// to X-Forwarded-For and the edge is the address the right-to-left walk would
// otherwise stop at. Trusting every CDN range instead would work too, and
// means maintaining a list of somebody else's netblocks forever.
// ---------------------------------------------------------------------------

// defaultClientIPHeader is used when trusted_proxies is set and no header is
// named. It is the header a plain reverse proxy sets.
const defaultClientIPHeader = "X-Forwarded-For"

// parseTrustedProxies turns the configured list into networks. A bare address
// is accepted and treated as a single host, because "172.18.0.1" is what an
// operator reads out of docker inspect and asking them to append /32 is a
// papercut with no upside.
func parseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(entries))
	for _, raw := range entries {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(e); err == nil {
			out = append(out, n)
			continue
		}
		ip := net.ParseIP(e)
		if ip == nil {
			return nil, fmt.Errorf("%q is neither an IP address nor a CIDR block", e)
		}
		bits := 32
		if ip.To4() == nil {
			bits = 128
		}
		out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return out, nil
}

func (c *Config) trusts(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range c.trustedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address to attribute a request to.
func (c *Config) ClientIP(r *http.Request) string {
	peer := peerIP(r)
	if c == nil || len(c.trustedNets) == 0 {
		return peer
	}
	if !c.trusts(net.ParseIP(peer)) {
		// The request did not come through a proxy we know about, so any
		// forwarding header on it was written by the caller.
		return peer
	}
	header := c.ClientIPHeader
	if header == "" {
		header = defaultClientIPHeader
	}
	candidates := forwardedChain(r, header)
	for i := len(candidates) - 1; i >= 0; i-- {
		ip := net.ParseIP(candidates[i])
		if ip == nil {
			continue
		}
		if c.trusts(ip) {
			continue
		}
		return ip.String()
	}
	// Every hop in the chain is a proxy we trust, or the header was absent
	// or unparseable. The peer is the most honest answer left.
	return peer
}

// forwardedChain collects the header's values in the order they were added,
// flattening repeated headers as well as comma separated ones.
func forwardedChain(r *http.Request, header string) []string {
	var out []string
	for _, v := range r.Header.Values(header) {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// An entry may carry a port, and an IPv6 entry may be bracketed.
			if h, _, err := net.SplitHostPort(part); err == nil {
				part = h
			}
			out = append(out, strings.Trim(part, "[]"))
		}
	}
	return out
}

// peerIP is the address of whoever opened the connection.
func peerIP(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(r.RemoteAddr, "[]")
}
