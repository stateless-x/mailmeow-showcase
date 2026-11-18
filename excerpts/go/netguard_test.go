package netguard

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestBlocked(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "::1", // loopback
		"10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.1.1", // RFC 1918
		"169.254.169.254", "169.254.0.1", "fe80::1", // link-local, incl. cloud metadata
		"100.64.0.1", "100.127.255.255", // CGNAT
		"fc00::1", "fd12:3456::1", // unique local
		"224.0.0.1", "239.255.255.255", "ff02::1", "ff0e::1", // multicast
		"0.0.0.0", "0.1.2.3", "::", // unspecified, "this network"
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:192.168.0.1", "::ffff:169.254.169.254", // IPv4-mapped
		"::ffff:100.64.0.1", "::ffff:0.0.0.0", "::ffff:224.0.0.1",
		"192.0.0.1", "198.18.0.1", "198.19.255.255", "240.0.0.1", "255.255.255.255", // reserved
		"64:ff9b::a00:1", "64:ff9b:1::1", "2002:a00:1::1", // NAT64 and 6to4 wrapping 10.0.0.1
	}
	for _, s := range blocked {
		if !Blocked(netip.MustParseAddr(s)) {
			t.Errorf("Blocked(%s) = false", s)
		}
	}
	public := []string{
		"93.184.216.34", "8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "2001:4860:4860::8888",
		"172.15.255.255", "172.32.0.0", "100.63.255.255", "100.128.0.0", "169.253.0.1", // just outside the ranges
		"::ffff:93.184.216.34",
	}
	for _, s := range public {
		if Blocked(netip.MustParseAddr(s)) {
			t.Errorf("Blocked(%s) = true", s)
		}
	}
}

type resolverFunc func(host string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return f(host)
}

func answers(m map[string][]string) resolverFunc {
	return func(host string) ([]netip.Addr, error) {
		ss, ok := m[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

func TestCheckURL(t *testing.T) {
	r := answers(map[string][]string{
		"hooks.example.com":    {"93.184.216.34"},
		"v6.example.com":       {"2606:4700:4700::1111"},
		"internal.example.com": {"10.0.0.1"}, // a name that points inside
		"metadata.example.com": {"169.254.169.254"},
		"mapped.example.com":   {"::ffff:10.0.0.1"},
		"mixed.example.com":    {"93.184.216.34", "192.168.0.5"},
		"empty.example.com":    {},
	})
	tests := []struct {
		url   string
		allow bool
		want  string // refusal code, "" for accepted
	}{
		{"https://hooks.example.com/mm", false, ""},
		{"https://v6.example.com:8443/x?y=1", false, ""},
		{"https://93.184.216.34/x", false, ""},
		{"https://[2606:4700:4700::1111]/x", false, ""},
		{"  https://hooks.example.com/mm  ", false, ""},

		{"http://hooks.example.com/mm", false, CodeNotHTTPS},
		{"ftp://hooks.example.com", false, CodeNotHTTPS},
		{"hooks.example.com/mm", false, CodeInvalidURL},
		{"", false, CodeInvalidURL},
		{"https://", false, CodeInvalidURL},
		{"https://user:pw@hooks.example.com/", false, CodeInvalidURL},

		{"https://127.0.0.1/", false, CodePrivateAddress},
		{"https://10.1.2.3/", false, CodePrivateAddress},
		{"https://172.20.0.1/", false, CodePrivateAddress},
		{"https://192.168.1.1/", false, CodePrivateAddress},
		{"https://169.254.169.254/latest/meta-data", false, CodePrivateAddress},
		{"https://100.64.0.1/", false, CodePrivateAddress},
		{"https://[::1]/", false, CodePrivateAddress},
		{"https://[fd00::1]/", false, CodePrivateAddress},
		{"https://[fe80::1]/", false, CodePrivateAddress},
		{"https://[ff02::1]/", false, CodePrivateAddress},
		{"https://0.0.0.0/", false, CodePrivateAddress},
		{"https://[::]/", false, CodePrivateAddress},
		{"https://[::ffff:127.0.0.1]/", false, CodePrivateAddress},
		{"https://[::ffff:10.0.0.1]/", false, CodePrivateAddress},
		{"https://[::ffff:169.254.169.254]/", false, CodePrivateAddress},
		{"https://localhost/", false, CodePrivateAddress},
		{"https://app.localhost/", false, CodePrivateAddress},
		{"https://internal.example.com/", false, CodePrivateAddress}, // DNS name resolving to 10.0.0.1
		{"https://metadata.example.com/", false, CodePrivateAddress},
		{"https://mapped.example.com/", false, CodePrivateAddress},
		{"https://mixed.example.com/", false, CodePrivateAddress}, // every answer is checked

		{"https://nope.example.com/", false, CodeUnresolvable},
		{"https://empty.example.com/", false, CodeUnresolvable},

		// Development: loopback receivers (incl. http://localhost) are allowed; every other
		// private address stays refused, and other http is not https.
		{"http://localhost:3000/x", true, ""},
		{"http://127.0.0.1:8080/x", true, ""},
		{"http://[::1]:8080/x", true, ""},
		{"https://10.0.0.5/x", true, CodePrivateAddress},
		{"https://metadata.example.com/", true, CodePrivateAddress},
		{"http://hooks.example.com/x", true, CodeNotHTTPS},
		{"http://10.0.0.5/x", true, CodeNotHTTPS},
		{"http://localhost.evil.com/", true, CodeNotHTTPS},
		// Without development, http://localhost is refused as not https.
		{"http://localhost:3000/x", false, CodeNotHTTPS},
	}
	for _, tt := range tests {
		err := CheckURL(context.Background(), r, tt.url, tt.allow)
		var ref *Refusal
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("CheckURL(%q, allow=%v) = %v, want ok", tt.url, tt.allow, err)
		case tt.want != "" && !errors.As(err, &ref):
			t.Errorf("CheckURL(%q, allow=%v) = %v, want refusal %s", tt.url, tt.allow, err, tt.want)
		case tt.want != "" && (ref.Code != tt.want || ref.Msg == ""):
			t.Errorf("CheckURL(%q, allow=%v) = %+v, want code %s", tt.url, tt.allow, ref, tt.want)
		}
	}
}

func TestControl(t *testing.T) {
	guard := Control(false)
	for addr, wantErr := range map[string]bool{
		"93.184.216.34:443": false, "[2606:4700:4700::1111]:443": false,
		"127.0.0.1:80": true, "10.0.0.1:443": true, "169.254.169.254:80": true,
		"[::1]:443": true, "[::ffff:10.0.0.1]:443": true, "[fd00::1]:443": true,
		"not-an-address": true, // a hostname here means DNS was skipped; refuse rather than guess
	} {
		err := guard("tcp", addr, nil)
		if (err != nil) != wantErr || (err != nil && !errors.Is(err, ErrBlockedAddress)) {
			t.Errorf("Control(%s) = %v, wantErr %v", addr, err, wantErr)
		}
	}
	if err := Control(true)("tcp", "127.0.0.1:80", nil); err != nil {
		t.Errorf("allowPrivate: %v", err)
	}
}

// The development allowance opens loopback only: the metadata address and the
// private network stay refused, both when an endpoint is added and on connect.
func TestAllowPrivateIsLoopbackOnly(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{"http://localhost:9009/hook", "http://127.0.0.1:9009/hook", "https://[::1]/hook"} {
		if err := CheckURL(ctx, nil, raw, true); err != nil {
			t.Errorf("CheckURL(%q, allowPrivate) = %v, want allowed", raw, err)
		}
	}
	for _, raw := range []string{"https://169.254.169.254/latest/meta-data", "https://10.0.0.1/hook", "https://[64:ff9b::a00:1]/hook"} {
		if err := CheckURL(ctx, nil, raw, true); err == nil {
			t.Errorf("CheckURL(%q, allowPrivate) allowed, want refused", raw)
		}
	}
	for _, addr := range []string{"169.254.169.254:443", "10.0.0.1:443", "[::ffff:192.168.1.1]:443"} {
		if err := Control(true)("tcp", addr, nil); err == nil {
			t.Errorf("Control(allowPrivate)(%q) allowed, want refused", addr)
		}
	}
}
