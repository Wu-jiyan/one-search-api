package provider

import (
	"net"
	"testing"
)

func TestNewOutboundRejectsInvalidEntries(t *testing.T) {
	if _, err := NewOutbound("", []string{"not-an-ip"}); err == nil {
		t.Fatal("expected an error for an invalid source address")
	}
	if _, err := NewOutbound("://bad", nil); err == nil {
		t.Fatal("expected an error for an invalid proxy")
	}
	out, err := NewOutbound("", nil)
	if err != nil {
		t.Fatalf("NewOutbound: %v", err)
	}
	if out.PoolSize() != 0 {
		t.Errorf("PoolSize = %d, want 0", out.PoolSize())
	}
	if out.Next() != "" {
		t.Errorf("Next on an empty pool = %q, want empty", out.Next())
	}
}

func TestOutboundRoundRobin(t *testing.T) {
	out, err := NewOutbound("", []string{"2602:f32a::1", "2602:f32a::2", "2602:f32a::1"})
	if err != nil {
		t.Fatalf("NewOutbound: %v", err)
	}
	if out.PoolSize() != 2 {
		t.Fatalf("PoolSize = %d, want 2 (duplicates removed)", out.PoolSize())
	}
	want := []string{"2602:f32a::1", "2602:f32a::2", "2602:f32a::1"}
	for i, w := range want {
		if got := out.Next(); got != w {
			t.Errorf("Next[%d] = %q, want %q", i, got, w)
		}
	}
}

func TestOutboundAutoExpandsHostAddresses(t *testing.T) {
	out, err := NewOutbound("", []string{"auto"})
	if err != nil {
		t.Fatalf("NewOutbound: %v", err)
	}
	// "auto" may legitimately find nothing on a host without global addresses,
	// but it must never produce an invalid address.
	for i := 0; i < 3; i++ {
		ip := out.Next()
		if ip == "" {
			continue
		}
		if net.ParseIP(ip) == nil {
			t.Fatalf("auto produced an invalid address %q", ip)
		}
	}
}

func TestSourceAddrSkipsLoopback(t *testing.T) {
	bind := net.ParseIP("2602:f32a::1")
	if addr := sourceAddrFor("127.0.0.1:80", bind); addr != nil {
		t.Errorf("sourceAddrFor(loopback) = %v, want nil", addr)
	}
	addr := sourceAddrFor("93.184.216.34:443", bind)
	if addr == nil {
		t.Fatal("sourceAddrFor(remote) = nil, want a source address")
	}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("sourceAddrFor returned %T, want *net.TCPAddr", addr)
	}
	if !tcp.IP.Equal(bind) {
		t.Errorf("source IP = %v, want %v", tcp.IP, bind)
	}
	if sourceAddrFor("93.184.216.34:443", nil) != nil {
		t.Error("sourceAddrFor without a bind address should return nil")
	}
}

func TestCachedClientIsSharedPerAddress(t *testing.T) {
	out, err := NewOutbound("", []string{"2602:f32a::1"})
	if err != nil {
		t.Fatalf("NewOutbound: %v", err)
	}
	a := out.CachedClient("2602:f32a::1", 0, "ua")
	b := out.CachedClient("2602:f32a::1", 0, "ua")
	if a != b {
		t.Error("cached client should be reused for the same source address")
	}
	c := out.CachedClient("2602:f32a::2", 0, "ua")
	if a == c {
		t.Error("different source addresses should get different clients")
	}
}
