package ssrf

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestAllowed(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"1.1.1.1", true},
		{"93.184.215.14", true},
		{"2606:4700:4700::1111", true},
		{"64:ff9b::808:808", true}, // NAT64 of 8.8.8.8
		{"2002:808:808::1", true},  // 6to4 of 8.8.8.8

		{"127.0.0.1", false},
		{"127.255.255.254", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"10.0.0.1", false},
		{"172.16.5.4", false},
		{"172.31.255.255", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false}, // cloud metadata
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		{"192.0.0.170", false},
		{"198.18.0.1", false},
		{"203.0.113.9", false},
		{"224.0.0.1", false},
		{"255.255.255.255", false},
		{"240.0.0.1", false},
		{"::", false},
		{"::1", false},
		{"::ffff:127.0.0.1", false},
		{"::ffff:10.1.2.3", false},
		{"::127.0.0.1", false},
		{"fe80::1", false},
		{"fc00::1", false},
		{"fd00:ec2::254", false}, // AWS IPv6 metadata
		{"ff02::1", false},
		{"64:ff9b::7f00:1", false},    // NAT64 of 127.0.0.1
		{"64:ff9b::a9fe:a9fe", false}, // NAT64 of 169.254.169.254
		{"64:ff9b:1::1", false},
		{"2002:7f00:1::1", false}, // 6to4 of 127.0.0.1
		{"2001::1", false},
		{"2001:db8::1", false},
		{"fec0::1", false},
	}
	for _, tt := range tests {
		if got := Allowed(netip.MustParseAddr(tt.ip)); got != tt.want {
			t.Errorf("Allowed(%s) = %v, want %v", tt.ip, got, tt.want)
		}
	}
	if Allowed(netip.Addr{}) {
		t.Error("zero Addr allowed")
	}
}

func TestControl(t *testing.T) {
	tests := []struct {
		network, address string
		ok               bool
	}{
		{"tcp", "1.1.1.1:443", true},
		{"tcp6", "[2606:4700:4700::1111]:443", true},
		{"tcp4", "127.0.0.1:80", false},
		{"tcp6", "[fe80::1%en0]:80", false},
		{"udp", "1.1.1.1:53", false},
		{"tcp", "not-an-address", false},
	}
	for _, tt := range tests {
		err := Control(tt.network, tt.address, nil)
		if (err == nil) != tt.ok || (err != nil && !errors.Is(err, ErrBlocked)) {
			t.Errorf("Control(%s, %s) = %v", tt.network, tt.address, err)
		}
	}
}

func TestDialerRefusesLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	d := net.Dialer{Control: Control}
	conn, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err == nil {
		_ = conn.Close()
		t.Fatal("dialed loopback")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("err = %v, want ErrBlocked", err)
	}
}

func FuzzAllowed(f *testing.F) {
	for _, s := range []string{"127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::7f00:1", "2002:a00:1::", "8.8.8.8"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ip, err := netip.ParseAddr(s)
		if err != nil || !Allowed(ip) {
			return
		}
		u := ip.Unmap()
		if u.IsLoopback() || u.IsPrivate() || u.IsLinkLocalUnicast() || u.IsUnspecified() || u.IsMulticast() {
			t.Errorf("Allowed(%s) for a non-public address", s)
		}
	})
}
