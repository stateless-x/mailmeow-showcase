// Package netguard keeps webhook deliveries away from internal networks (SSRF).
// Two checks work together: CheckURL when an endpoint is added, and Control at
// connect time, so a DNS answer that changes later (rebinding) can't get through.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// Refusal explains why a URL was refused. Code names the reason for the API;
// Msg is a plain sentence for the person who typed the URL.
type Refusal struct{ Code, Msg string }

func (r *Refusal) Error() string { return r.Msg }

// Refusal codes.
const (
	CodeInvalidURL     = "webhook_url_invalid"
	CodeNotHTTPS       = "webhook_url_not_https"
	CodePrivateAddress = "webhook_url_private"
	CodeUnresolvable   = "webhook_url_unresolvable"
)

// ErrBlockedAddress is what the dialer returns for a blocked connection.
var ErrBlockedAddress = errors.New("refused to connect to a private or internal address")

// Resolver is the part of *net.Resolver that CheckURL uses.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),   // reserved, incl. 255.255.255.255
	// IPv6 ranges that carry an IPv4 address inside them, so a gateway can
	// translate them to an internal IPv4 host.
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 well-known prefix
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64 local-use prefix
	netip.MustParsePrefix("2002::/16"),      // 6to4
}

// Blocked reports whether ip is an address a webhook must never reach:
// loopback, private (RFC 1918 and fc00::/7), link-local (including the cloud
// metadata address 169.254.169.254), CGNAT, multicast or unspecified. An
// IPv4-mapped IPv6 address (::ffff:10.0.0.1) is judged as its IPv4 address.
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func isLocalhost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// blockedFor is Blocked with the development exception: allowPrivate lets a
// webhook reach loopback (a receiver on this machine) and nothing else, so even
// in development the cloud metadata address and the private network stay closed.
func blockedFor(ip netip.Addr, allowPrivate bool) bool {
	if allowPrivate && ip.Unmap().IsLoopback() {
		return false
	}
	return Blocked(ip)
}

// CheckURL decides whether raw may be saved as a webhook endpoint. It must be
// https with a public host: an IP literal, or a name whose every resolved
// address is public. allowPrivate (development only) also allows loopback
// receivers (http or https on localhost), never other private addresses.
func CheckURL(ctx context.Context, r Resolver, raw string, allowPrivate bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return &Refusal{CodeInvalidURL, "This isn't a valid web address. It should look like https://example.com/hooks."}
	}
	if u.User != nil {
		return &Refusal{CodeInvalidURL, "The address can't contain a username or password."}
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowPrivate && isLocalhost(host):
	default:
		return &Refusal{CodeNotHTTPS, "The address must start with https://."}
	}
	private := &Refusal{CodePrivateAddress, "This address points to a private network. Use a public address."}
	if ip, err := netip.ParseAddr(host); err == nil {
		if blockedFor(ip, allowPrivate) {
			return private
		}
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		if allowPrivate {
			return nil
		}
		return private
	}
	ips, err := r.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return &Refusal{CodeUnresolvable, "We couldn't find this address. Check the spelling, or that its DNS record exists."}
	}
	for _, ip := range ips {
		if blockedFor(ip, allowPrivate) {
			return private
		}
	}
	return nil
}

// Control is a net.Dialer.Control hook. It runs with the address the dialer is
// about to connect to, after DNS, so it sees the IP that will really be used.
func Control(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: %q", ErrBlockedAddress, address)
		}
		if blockedFor(ap.Addr(), allowPrivate) {
			return ErrBlockedAddress
		}
		return nil
	}
}
