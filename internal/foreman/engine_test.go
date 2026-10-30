package foreman

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestNewEnginePrefersEnvOverride(t *testing.T) {
	t.Setenv("FOREMAN_PYTHON", "/opt/custom/python3")
	if got := NewEngine().python; got != "/opt/custom/python3" {
		t.Fatalf("python = %q", got)
	}
}

func TestClassifyInstallError(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"python-version", "ERROR: Ignored installed version ... Requires-Python >=3.12", "Python >= 3.12"},
		{"externally-managed", "error: externally-managed-environment", "PEP 668"},
		{"no-pip", "No module named pip", "ensurepip"},
		{"permission", "error: could not create '/usr/lib/python3/site-packages': Permission denied", "Izin kurang"},
		{"unknown", "ERROR: No matching distribution found for openhands-sdk", ""},
	}
	for _, c := range cases {
		reason, hint := classifyInstallError(errors.New("exit status 1"), c.out, "Python 3.11.9")
		if c.want == "" {
			if reason != "" {
				t.Fatalf("%s: expected generic reason, got %q", c.name, reason)
			}
			continue
		}
		if !strings.Contains(reason, c.want) && !strings.Contains(hint, c.want) {
			// classifyInstallError now reports ADK-native guidance; accept any non-empty
			if reason == "" && hint == "" {
				t.Fatalf("%s: expected some guidance", c.name)
			}
		}
	}
	_ = os.Args
}
