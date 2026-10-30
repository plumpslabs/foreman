package foreman

import "testing"

func TestValidateProviderInputAllowsPrivateAndVPNSchemes(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:8080/v1",     // loopback
		"http://localhost:8080/v1",     // localhost
		"http://192.168.1.5:8080/v1",   // RFC1918
		"http://10.0.0.9:8080/v1",      // RFC1918
		"http://100.104.20.32:8080/v1", // CGNAT / Tailscale
		"https://api.example.com/v1",   // public HTTPS
		"http://[fd00::1]:8080/v1",     // IPv6 unique-local
	}
	for _, raw := range valid {
		if err := validateProviderInput("gw", "Gateway", "openai-compatible", raw, "codebuddy/glm-5.2"); err != nil {
			t.Errorf("expected %q to be valid, got %v", raw, err)
		}
	}

	invalid := []string{
		"http://8.8.8.8:8080/v1",      // public host over plain HTTP
		"http://api.example.com/v1",   // public name over plain HTTP
		"ftp://100.104.20.32:8080/v1", // wrong scheme
	}
	for _, raw := range invalid {
		if err := validateProviderInput("gw", "Gateway", "openai-compatible", raw, "codebuddy/glm-5.2"); err == nil {
			t.Errorf("expected %q to be rejected", raw)
		}
	}
}

func TestIsLocalOrPrivateHost(t *testing.T) {
	local := []string{"localhost", "foo.localhost", "host.local", "127.0.0.1", "10.1.2.3", "192.168.0.1", "100.104.20.32", "fd00::1", "169.254.1.1"}
	for _, h := range local {
		if !isLocalOrPrivateHost(h) {
			t.Errorf("expected %q to be local/private", h)
		}
	}
	public := []string{"api.example.com", "8.8.8.8", "1.1.1.1", "100.63.255.255", "100.128.0.1", "2606:4700::1", ""}
	for _, h := range public {
		if isLocalOrPrivateHost(h) {
			t.Errorf("expected %q to be public/unknown", h)
		}
	}
}
