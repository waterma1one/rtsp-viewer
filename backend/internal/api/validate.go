package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

const maxURLLength = 2048

// ErrInvalidURL is wrapped by every validation failure so callers can map it
// to a 400 response without string matching.
var ErrInvalidURL = errors.New("invalid stream url")

// Resolver is the subset of net.Resolver used by URLValidator; tests swap it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// URLValidator decides whether the server may open an RTSP connection to a
// user-supplied URL. The server dials whatever the user types, so without
// these checks it would be an SSRF proxy into the hosting provider's network.
type URLValidator struct {
	Resolver Resolver
	// AllowPrivate disables the private-address check (local development).
	AllowPrivate bool
	// AllowedHostPorts are "host:port" pairs exempt from the private-address
	// check, e.g. the bundled demo server at 127.0.0.1:8554.
	AllowedHostPorts map[string]bool
}

// Validate returns the normalised URL string or an error wrapping ErrInvalidURL.
func (v *URLValidator) Validate(ctx context.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: url is empty", ErrInvalidURL)
	}
	if len(raw) > maxURLLength {
		return "", fmt.Errorf("%w: url longer than %d characters", ErrInvalidURL, maxURLLength)
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ' ' }) {
		return "", fmt.Errorf("%w: url contains whitespace or control characters", ErrInvalidURL)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "rtsp" && scheme != "rtsps" {
		return "", fmt.Errorf("%w: scheme must be rtsp:// or rtsps://", ErrInvalidURL)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	port := u.Port()
	if port == "" {
		port = "554"
		if scheme == "rtsps" {
			port = "322"
		}
	}

	if v.AllowPrivate || v.AllowedHostPorts[net.JoinHostPort(host, port)] {
		return u.String(), nil
	}

	addrs, err := v.resolve(ctx, host)
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve host %q", ErrInvalidURL, host)
	}
	for _, a := range addrs {
		if !isPublic(a) {
			return "", fmt.Errorf("%w: host %q resolves to a private or reserved address", ErrInvalidURL, host)
		}
	}
	return u.String(), nil
}

func (v *URLValidator) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	r := v.Resolver
	if r == nil {
		r = net.DefaultResolver
	}
	addrs, err := r.LookupNetIP(ctx, "ip", host)
	if err == nil && len(addrs) == 0 {
		err = errors.New("no addresses")
	}
	return addrs, err
}

// cgnat is 100.64.0.0/10, which netip does not classify as private but which
// cloud providers use internally.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isPublic(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() &&
		!a.IsLoopback() &&
		!a.IsPrivate() &&
		!a.IsLinkLocalUnicast() &&
		!a.IsLinkLocalMulticast() &&
		!a.IsInterfaceLocalMulticast() &&
		!a.IsMulticast() &&
		!a.IsUnspecified() &&
		!cgnat.Contains(a)
}
