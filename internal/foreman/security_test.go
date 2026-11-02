package foreman

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestProviderTestSurfacesAuthError(t *testing.T) {
	// A provider that answers /models with 401 must surface a clear key error,
	// not a generic "connection failed" — this is exactly the Zro/MoonMath
	// case where an invalid or expired key returns 401.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := ProviderConfig{
		ID:           "zro-test",
		Name:         "Zro (MoonMath)",
		Protocol:     "openai-compatible",
		BaseURL:      srv.URL,
		DefaultModel: "deepseek-v4.1-flash",
	}
	err := testProvider(context.Background(), cfg, "sk-invalid")
	if err == nil {
		t.Fatal("expected an error for a 401 provider response")
	}
	msg := err.Error()
	if !strings.Contains(msg, "401") && !strings.Contains(msg, "rejected the API key") {
		t.Fatalf("expected a clear auth error, got %q", msg)
	}
}

// TestProviderMultiKeySelection verifies that a provider with several named keys
// uses the first ENABLED key at run time, and falls back to the legacy default
// when no keys are configured. No rotation logic is exercised here — the agent
// profile owns fallover.
func TestProviderMultiKeySelection(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := ProviderConfig{
		ID: "zro", Name: "Zro (MoonMath)", Protocol: "openai-compatible",
		BaseURL: "https://zro.moonmath.ai/v1", DefaultModel: "deepseek-v4.1-flash",
		Enabled: true,
	}
	if _, err := store.SaveProvider(config, ""); err != nil {
		t.Fatal(err)
	}
	// Default (legacy) credential works when no named keys exist.
	if err := store.saveProviderCredential(store.Snapshot().ProjectPath, "zro", "legacy-key"); err != nil {
		t.Fatal(err)
	}
	if got := store.ProviderSecretForRun(store.Snapshot().ProjectPath, config); got != "legacy-key" {
		t.Fatalf("expected legacy key, got %q", got)
	}

	// Add two named keys; both enabled, first should win.
	if err := store.SaveProviderKey("zro", "key 1", "secret-one", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviderKey("zro", "key 2", "secret-two", true); err != nil {
		t.Fatal(err)
	}
	reloaded := store.ProvidersForAPI()
	if len(reloaded) != 1 || len(reloaded[0].Keys) != 2 {
		t.Fatalf("expected 2 named keys, got %+v", reloaded)
	}
	if got := store.ProviderSecretForRun(store.Snapshot().ProjectPath, reloaded[0]); got != "secret-one" {
		t.Fatalf("expected first enabled key, got %q", got)
	}

	// Disable key 1 → engine must pick key 2.
	if err := store.SaveProviderKey("zro", "key 1", "", false); err != nil {
		t.Fatal(err)
	}
	reloaded = store.ProvidersForAPI()
	if got := store.ProviderSecretForRun(store.Snapshot().ProjectPath, reloaded[0]); got != "secret-two" {
		t.Fatalf("expected second key after disabling first, got %q", got)
	}

	// Disable both → no secret available.
	if err := store.SaveProviderKey("zro", "key 2", "", false); err != nil {
		t.Fatal(err)
	}
	reloaded = store.ProvidersForAPI()
	if got := store.ProviderSecretForRun(store.Snapshot().ProjectPath, reloaded[0]); got != "" {
		t.Fatalf("expected empty secret when all keys disabled, got %q", got)
	}

	// Deleting a named key removes its credential.
	if err := store.SaveProviderKey("zro", "key 3", "secret-three", true); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProviderKey("zro", "key 3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.loadProviderKey(store.Snapshot().ProjectPath, "zro", "key 3"); got != "" {
		t.Fatalf("deleted key credential should be gone, got %q", got)
	}
}

// TestProviderSecretForRunHonorsFirstEnabledAcrossProviders checks that
// FirstEnabledProviderSecret walks providers and picks the first enabled key.
func TestProviderSecretForRunHonorsFirstEnabledAcrossProviders(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p1 := ProviderConfig{ID: "zro-a", Name: "Zro A", Protocol: "openai-compatible",
		BaseURL: "https://zro.moonmath.ai/v1", DefaultModel: "deepseek-v4.1-flash", Enabled: true}
	p2 := ProviderConfig{ID: "zro-b", Name: "Zro B", Protocol: "openai-compatible",
		BaseURL: "https://zro.moonmath.ai/v1", DefaultModel: "glm-5.3", Enabled: true}
	if _, err := store.SaveProvider(p1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveProvider(p2, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviderKey("zro-a", "key 1", "aaa", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviderKey("zro-b", "key 1", "bbb", true); err != nil {
		t.Fatal(err)
	}
	cfg, secret, ok := store.FirstEnabledProviderSecret()
	if !ok {
		t.Fatal("expected an enabled provider with a key")
	}
	// The first enabled provider must return its first enabled key's secret.
	// Insertion order is not guaranteed, so just assert it is one of the two.
	if secret != "aaa" && secret != "bbb" {
		t.Fatalf("unexpected secret %q for provider %q", secret, cfg.ID)
	}
	// And it must match the secret the provider itself reports.
	byID := map[string]string{"zro-a": "aaa", "zro-b": "bbb"}
	if secret != byID[cfg.ID] {
		t.Fatalf("FirstEnabledProviderSecret returned %q for %q but expected %q", secret, cfg.ID, byID[cfg.ID])
	}
}

// TestSaveProviderPreservesNamedKeys guards a regression where saving or
// toggling a provider (which omits Keys) wiped every named account.
func TestSaveProviderPreservesNamedKeys(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := ProviderConfig{
		ID: "zro", Name: "Zro (MoonMath)", Protocol: "openai-compatible",
		BaseURL: "https://zro.moonmath.ai/v1", DefaultModel: "deepseek-v4.1-flash",
		Enabled: true,
	}
	if _, err := store.SaveProvider(base, "legacy"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviderKey("zro", "key 1", "s1", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviderKey("zro", "key 2", "s2", true); err != nil {
		t.Fatal(err)
	}

	// A plain save from the UI (no Keys field) must keep both accounts.
	toggle := base
	toggle.Enabled = false
	if _, err := store.SaveProvider(toggle, ""); err != nil {
		t.Fatal(err)
	}
	got := store.ProvidersForAPI()
	if len(got) != 1 || len(got[0].Keys) != 2 {
		t.Fatalf("named keys were wiped by a plain save: %+v", got)
	}
	// And the run-time secret still resolves from the first enabled key.
	if secret := store.ProviderSecretForRun(store.Snapshot().ProjectPath, got[0]); secret != "s1" {
		t.Fatalf("expected first enabled key after save, got %q", secret)
	}
}

func TestProviderKeyPreviewAndMask(t *testing.T) {
	if got := maskSecret(""); got != "" {
		t.Fatalf("empty secret mask = %q", got)
	}
	if got := maskSecret("short"); got != "••••" {
		t.Fatalf("short secret mask = %q", got)
	}
	got := maskSecret("sk-abcdefgh1234")
	if got != "sk-…1234" {
		t.Fatalf("mask = %q", got)
	}
	// The mask must never contain the full secret.
	if strings.Contains(got, "abcdefgh") {
		t.Fatalf("mask leaked the secret: %q", got)
	}

	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := ProviderConfig{ID: "tok", Name: "TokenHarbor", Protocol: "openai-compatible",
		BaseURL: "https://tokenharbor.ai/v1", DefaultModel: "m", Enabled: true}
	if _, err := store.SaveProvider(cfg, "thk_default_secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviderKey("tok", "TokenHarbor 1", "thk_named_secret", true); err != nil {
		t.Fatal(err)
	}
	list := store.ProvidersForAPI()
	if len(list) != 1 {
		t.Fatalf("providers = %d", len(list))
	}
	if list[0].DefaultKeyPreview == "" || strings.Contains(list[0].DefaultKeyPreview, "default_secret") {
		t.Fatalf("default preview wrong: %q", list[0].DefaultKeyPreview)
	}
	if len(list[0].Keys) != 1 || list[0].Keys[0].Preview == "" || strings.Contains(list[0].Keys[0].Preview, "named_secret") {
		t.Fatalf("named preview wrong: %+v", list[0].Keys)
	}
}

// Toggling an account on or off must never touch its stored secret. The UI
// sends an empty apiKey when it only flips the switch, and that empty string
// used to delete the credential — the account silently lost its key.
func TestProviderKeyToggleKeepsSecret(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := ProviderConfig{
		ID: "zro", Name: "Zro", Protocol: "openai-compatible",
		BaseURL: "https://zro.moonmath.ai/v1", DefaultModel: "deepseek-v4.1-flash", Enabled: true,
	}
	if _, err := store.SaveProvider(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveProviderKey("zro", "Zro 1", "secret-one", true); err != nil {
		t.Fatal(err)
	}

	// Off, then back on, both times with an empty apiKey (what the toggle sends).
	if err := store.SaveProviderKey("zro", "Zro 1", "", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.loadProviderKey(store.Snapshot().ProjectPath, "zro", "Zro 1"); got != "secret-one" {
		t.Fatalf("switching an account off must keep its key, got %q", got)
	}
	if err := store.SaveProviderKey("zro", "Zro 1", "", true); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.loadProviderKey(store.Snapshot().ProjectPath, "zro", "Zro 1"); got != "secret-one" {
		t.Fatalf("switching an account back on must keep its key, got %q", got)
	}

	// The account still carries a masked preview, so the UI can show it.
	reloaded := store.ProvidersForAPI()
	if len(reloaded) != 1 || len(reloaded[0].Keys) != 1 {
		t.Fatalf("expected the account to survive the toggle, got %+v", reloaded)
	}
	if reloaded[0].Keys[0].Preview == "" {
		t.Fatal("the account lost its masked preview after toggling")
	}

	// Adding a brand-new account without a secret must be rejected instead of
	// creating a key row that can never run.
	if err := store.SaveProviderKey("zro", "Zro 2", "", true); err == nil {
		t.Fatal("adding an account without an API key should fail")
	}
}

// Replacing an account's key must keep the account's name and its position in
// the rotation. Delete-then-add used to be the only path, which reordered the
// account and reset its name.
func TestProviderKeyReplaceKeepsNameAndOrder(t *testing.T) {
	store, err := openStoreForTest(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := ProviderConfig{
		ID: "zro", Name: "Zro", Protocol: "openai-compatible",
		BaseURL: "https://zro.moonmath.ai/v1", DefaultModel: "m", Enabled: true,
	}
	if _, err := store.SaveProvider(cfg, ""); err != nil {
		t.Fatal(err)
	}
	for _, kv := range []struct{ name, secret string }{
		{"first", "secret-one"}, {"second", "secret-two"}, {"third", "secret-three"},
	} {
		if err := store.SaveProviderKey("zro", kv.name, kv.secret, true); err != nil {
			t.Fatal(err)
		}
	}

	// Replace the middle account's secret in place.
	if err := store.SaveProviderKey("zro", "second", "secret-two-rotated", true); err != nil {
		t.Fatal(err)
	}

	saved := store.ProvidersForAPI()[0]
	var names []string
	for _, k := range saved.Keys {
		names = append(names, k.Name)
	}
	if len(names) != 3 || names[0] != "first" || names[1] != "second" || names[2] != "third" {
		t.Fatalf("account order changed after a key replace: %v", names)
	}
	if got, _ := store.loadProviderKey(store.Snapshot().ProjectPath, "zro", "second"); got != "secret-two-rotated" {
		t.Fatalf("stored secret = %q, want the replacement", got)
	}
	if got, _ := store.loadProviderKey(store.Snapshot().ProjectPath, "zro", "first"); got != "secret-one" {
		t.Fatalf("replacing one account must not touch another, got %q", got)
	}

	// The rotation must still see three distinct accounts in the same order.
	keys, err := store.runKeysLocked(saved)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[1].Secret != "secret-two-rotated" {
		t.Fatalf("rotation pool = %+v, want the rotated secret in slot 2", keys)
	}
}
