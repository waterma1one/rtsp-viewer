package api

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, errors.New("not found")
}

func TestValidate(t *testing.T) {
	v := &URLValidator{
		Resolver: fakeResolver{
			"cam.example.com":  {netip.MustParseAddr("93.184.216.34")},
			"internal.example": {netip.MustParseAddr("10.0.0.5")},
			"mixed.example":    {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("192.168.1.1")},
		},
		AllowedHostPorts: map[string]bool{"127.0.0.1:8554": true},
	}

	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"public host", "rtsp://cam.example.com:554/live", true},
		{"public ip with creds", "rtsp://user:pass@93.184.216.34/stream", true},
		{"rtsps", "rtsps://cam.example.com/live", true},
		{"uppercase scheme", "RTSP://cam.example.com/live", true},
		{"allowlisted demo", "rtsp://127.0.0.1:8554/cam1", true},
		{"loopback other port", "rtsp://127.0.0.1:8080/x", false},
		{"loopback default port", "rtsp://127.0.0.1/x", false},
		{"private ip", "rtsp://192.168.0.10/stream", false},
		{"metadata ip", "rtsp://169.254.169.254/latest", false},
		{"cgnat", "rtsp://100.64.1.1/x", false},
		{"ipv6 loopback", "rtsp://[::1]:8554/x", false},
		{"v4-mapped v6 private", "rtsp://[::ffff:10.0.0.1]/x", false},
		{"dns to private", "rtsp://internal.example/x", false},
		{"dns mixed", "rtsp://mixed.example/x", false},
		{"unresolvable", "rtsp://nope.invalid/x", false},
		{"http scheme", "http://cam.example.com/x", false},
		{"file scheme", "file:///etc/passwd", false},
		{"no scheme", "cam.example.com/live", false},
		{"empty", "   ", false},
		{"space inside", "rtsp://cam.example.com/a b", false},
		{"newline", "rtsp://cam.example.com/a\n-i", false},
		{"no host", "rtsp:///path", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.Validate(context.Background(), tc.in)
			if tc.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				if !errors.Is(err, ErrInvalidURL) {
					t.Fatalf("error does not wrap ErrInvalidURL: %v", err)
				}
			}
		})
	}
}

func TestValidateAllowPrivate(t *testing.T) {
	v := &URLValidator{AllowPrivate: true}
	if _, err := v.Validate(context.Background(), "rtsp://192.168.1.20:8554/cam"); err != nil {
		t.Fatalf("AllowPrivate should accept private hosts: %v", err)
	}
	if _, err := v.Validate(context.Background(), "http://192.168.1.20/cam"); err == nil {
		t.Fatal("AllowPrivate must still enforce scheme")
	}
}
