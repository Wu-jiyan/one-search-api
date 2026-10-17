package fetchguard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateURL(t *testing.T) {
	g := New(false, []int{80, 443, 8080, 8443}, 5)
	cases := []struct {
		url     string
		wantErr error
	}{
		{"https://example.com/path", nil},
		{"http://example.com:8080/x", nil},
		{"file:///etc/passwd", ErrInvalid},
		{"ftp://example.com/", ErrInvalid},
		{"https://", ErrInvalid},
		{"https://user:pass@example.com/", ErrInvalid},
		{"https://example.com:22/", ErrBlocked},
		{"http://127.0.0.1/", ErrBlocked},
		{"http://localhost:8080/", ErrBlocked},
		{"http://localhost.localdomain/", ErrBlocked},
		{"http://169.254.169.254/latest/meta-data/", ErrBlocked},
		{"http://[::1]/", ErrBlocked},
		{"http://0.0.0.0/", ErrBlocked},
		{"http://10.1.2.3/", ErrBlocked},
		{"http://192.168.1.1/", ErrBlocked},
		{"http://172.16.0.1/", ErrBlocked},
		{"http://100.64.0.1/", ErrBlocked},
		{"http://metadata.google.internal/", ErrBlocked},
		{"http://service.internal/", ErrBlocked},
		{"https://" + strings.Repeat("a", 8200) + ".com/", ErrInvalid},
	}
	for _, tc := range cases {
		_, err := g.ValidateURL(tc.url)
		if tc.wantErr == nil {
			if err != nil {
				t.Errorf("ValidateURL(%q) = %v, want nil", tc.url, err)
			}
			continue
		}
		if !errors.Is(err, tc.wantErr) {
			t.Errorf("ValidateURL(%q) = %v, want %v", tc.url, err, tc.wantErr)
		}
	}
}

func TestAllowPrivate(t *testing.T) {
	g := New(true, []int{80, 443, 8080}, 5)
	if _, err := g.ValidateURL("http://127.0.0.1:8080/"); err != nil {
		t.Errorf("allow_private should permit loopback: %v", err)
	}
}

func TestDialContextBlocksLoopback(t *testing.T) {
	g := New(false, []int{80, 443}, 5)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := g.DialContext(ctx, "tcp", "127.0.0.1:80")
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("DialContext error = %v, want ErrBlocked", err)
	}
}

func TestDialContextBlocksPort(t *testing.T) {
	g := New(true, []int{443}, 5)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := g.DialContext(ctx, "tcp", "127.0.0.1:22")
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("DialContext error = %v, want ErrBlocked", err)
	}
}

func TestClassifyError(t *testing.T) {
	if got := ClassifyError(ErrBlocked); got != 403 {
		t.Errorf("Blocked -> %d", got)
	}
	if got := ClassifyError(ErrInvalid); got != 400 {
		t.Errorf("Invalid -> %d", got)
	}
	if got := ClassifyError(errors.New("x")); got != 502 {
		t.Errorf("other -> %d", got)
	}
}
