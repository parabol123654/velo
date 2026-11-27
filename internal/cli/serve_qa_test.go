package cli

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStderr runs fn and returns what it wrote to stderr.
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	runErr := fn()
	os.Stderr = saved
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// `velo serve -h` and a bad --trust-proxy value both stop at flag parsing,
// before any database or listener, so the flag can be checked without a server.
func TestQAServeTrustProxyFlag(t *testing.T) {
	for _, tc := range []struct {
		env, wantDefault string
	}{
		{"", ""},
		{"1", "(default true)"},
		{"true", "(default true)"},
		{"TRUE", "(default true)"},
		{"0", ""},
		{"false", ""},
		{"yes", ""}, // not a Go bool: ignored, flag stays off
		{" 1", ""},
	} {
		t.Setenv("VELO_TRUST_PROXY", tc.env)
		out, err := captureStderr(t, func() error { return runServe([]string{"-h"}) })
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("env=%q: runServe -h: %v", tc.env, err)
		}
		i := strings.Index(out, "-trust-proxy")
		if i < 0 {
			t.Fatalf("env=%q: no -trust-proxy in usage:\n%s", tc.env, out)
		}
		line := out[i:]
		if j := strings.Index(line, "\n  -"); j >= 0 {
			line = line[:j]
		}
		if !strings.Contains(line, "VELO_TRUST_PROXY") {
			t.Errorf("usage does not mention VELO_TRUST_PROXY: %s", line)
		}
		if got := strings.Contains(line, "(default true)"); got != (tc.wantDefault != "") {
			t.Errorf("env=%q: default true shown=%v: %s", tc.env, got, line)
		}
	}

	_, err := captureStderr(t, func() error { return runServe([]string{"--trust-proxy=maybe"}) })
	if err == nil {
		t.Error("--trust-proxy=maybe accepted")
	}
}
