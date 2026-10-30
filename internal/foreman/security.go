package foreman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

func validateProviderInput(id, name, protocol, rawURL, model string) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" || strings.TrimSpace(model) == "" || len(id) > 80 || len(name) > 120 || len(model) > 200 || len(rawURL) > 2048 {
		return errors.New("provider id, name, and default model are required")
	}
	if id != strings.ToLower(strings.TrimSpace(id)) {
		return errors.New("provider id must be lowercase")
	}
	if protocol != "openai-compatible" && protocol != "anthropic" {
		return errors.New("protocol must be openai-compatible or anthropic")
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("provider URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return errors.New("provider URL must use HTTP or HTTPS")
	}
	// Plain HTTP is allowed for a loopback, LAN, or VPN-overlay endpoint (e.g. a
	// Tailscale gateway at 100.x): that traffic never crosses the public
	// internet, so it does not need TLS to stay private. Public hosts must use
	// HTTPS.
	if u.Scheme == "http" && !isLocalOrPrivateHost(u.Hostname()) {
		return errors.New("plain HTTP is allowed only for a local or private/VPN provider endpoint")
	}
	return nil
}

// cgnatRange is the Carrier-Grade NAT block (100.64.0.0/10) used by Tailscale
// and other VPN overlays. These addresses are not publicly routable.
var cgnatRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isLocalOrPrivateHost reports whether host is reachable only on the local
// machine or a private network/VPN overlay: loopback, RFC1918 private,
// link-local, CGNAT/Tailscale (100.64.0.0/10), or IPv6 unique-local addresses,
// or a localhost/.local name.
func isLocalOrPrivateHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && cgnatRange.Contains(ip4) {
		return true
	}
	return false
}

func validateProviderDestination(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid provider URL")
	}
	host := strings.ToLower(u.Hostname())
	// An explicitly configured local/private/VPN endpoint is allowed (e.g. a
	// Tailscale gateway). This branch is an IP literal or localhost/.local name.
	if isLocalOrPrivateHost(host) {
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve provider host: %w", err)
	}
	if len(ips) == 0 {
		return errors.New("provider host did not resolve")
	}
	// A public name must still resolve only to public addresses; this blocks
	// DNS-rebinding attempts that hide an internal target behind a public name.
	for _, addr := range ips {
		if !addr.IP.IsGlobalUnicast() || addr.IP.IsPrivate() || addr.IP.IsLoopback() || addr.IP.IsLinkLocalUnicast() {
			return errors.New("provider endpoints must not resolve to private or local network addresses")
		}
	}
	return nil
}

// credentialsPath is the global credential file (next to the global profile).
func (s *Store) credentialsPath() string {
	return filepath.Join(filepath.Dir(s.globalPath), "credentials.json")
}

func (s *Store) saveProviderCredential(projectPath, providerID, secret string) error {
	path := s.credentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	credentials := map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &credentials); err != nil {
			return fmt.Errorf("read provider credentials: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if secret == "" {
		delete(credentials, providerID)
	} else {
		credentials[providerID] = secret
	}
	data, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return nil
}

func (s *Store) deleteProviderCredential(projectPath, providerID string) error {
	return s.saveProviderCredential(projectPath, providerID, "")
}

func (s *Store) loadProviderCredential(projectPath, providerID string) (string, error) {
	path := s.credentialsPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// Legacy: try the project-local credential file.
		legacy := filepath.Join(projectPath, ".foreman", "credentials.json")
		data, err = os.ReadFile(legacy)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
	}
	if err != nil {
		return "", err
	}
	credentials := map[string]string{}
	if err := json.Unmarshal(data, &credentials); err != nil {
		return "", fmt.Errorf("read provider credentials: %w", err)
	}
	return credentials[providerID], nil
}

func (s *Store) SaveProvider(config ProviderConfig, secret string) (ProviderConfig, error) {
	if secret != "" && (len(secret) > 4096 || strings.ContainsAny(secret, "\r\n")) {
		return ProviderConfig{}, errors.New("provider API key must be a single line under 4096 characters")
	}
	if _, err := s.UpsertProvider(config); err != nil {
		return ProviderConfig{}, err
	}
	if secret != "" {
		if err := s.saveProviderCredential(s.Snapshot().ProjectPath, config.ID, secret); err != nil {
			return ProviderConfig{}, err
		}
		s.SetProviderSecretFlag(config.ID, true)
		config.HasAPIKey = true
	} else {
		// Preserve an existing credential when the user leaves the key blank.
		stored, err := s.loadProviderCredential(s.Snapshot().ProjectPath, config.ID)
		config.HasAPIKey = err == nil && stored != ""
	}
	if config.ID == "" {
		config.ID = uuid.NewString()
	}
	return config, nil
}

// DeleteProvider removes a provider, its saved credential, and any references
// from agent profiles and team rosters.
func (s *Store) DeleteProvider(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.state.Providers
	filtered := make([]ProviderConfig, 0, len(previous))
	found := false
	for _, provider := range previous {
		if provider.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, provider)
	}
	if !found {
		return fmt.Errorf("unknown provider id %q", id)
	}
	previousAgents := s.state.Agents
	previousTeams := s.state.Teams
	s.state.Providers = filtered
	s.state.Agents = append([]Agent(nil), previousAgents...)
	for i := range s.state.Agents {
		if s.state.Agents[i].ProviderID == id {
			s.state.Agents[i].ProviderID = ""
		}
	}
	s.state.Teams = append([]Team(nil), previousTeams...)
	for i := range s.state.Teams {
		s.state.Teams[i].Agents = append([]Agent(nil), previousTeams[i].Agents...)
		for j := range s.state.Teams[i].Agents {
			if s.state.Teams[i].Agents[j].ProviderID == id {
				s.state.Teams[i].Agents[j].ProviderID = ""
			}
		}
	}
	if err := s.save(); err != nil {
		s.state.Providers = previous
		s.state.Agents = previousAgents
		s.state.Teams = previousTeams
		return err
	}
	_ = s.deleteProviderCredential(s.state.ProjectPath, id)
	return nil
}

func (s *Store) ProvidersForAPI() []ProviderConfig {
	state := s.Snapshot()
	for i := range state.Providers {
		secret, err := s.loadProviderCredential(state.ProjectPath, state.Providers[i].ID)
		state.Providers[i].HasAPIKey = err == nil && secret != ""
	}
	return state.Providers
}

func testProvider(ctx context.Context, config ProviderConfig, secret string) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := validateProviderDestination(ctx, config.BaseURL); err != nil {
		return err
	}
	if config.Protocol == "anthropic" {
		return anthropicPing(ctx, config, secret)
	}
	models, listErr := fetchOpenAIModels(ctx, config.BaseURL, secret)
	if listErr == nil {
		for _, model := range models {
			if model == config.DefaultModel {
				return nil
			}
		}
	}
	// Some gateways do not expose /models. Fall back to a minimal chat request.
	if pingErr := openAIChatPing(ctx, config, secret); pingErr != nil {
		if listErr == nil {
			return fmt.Errorf("connected, but model %q was neither listed nor accepted: %v", config.DefaultModel, pingErr)
		}
		return errors.New("provider connection failed; check the endpoint, network, and credentials")
	}
	return nil
}

func anthropicPing(ctx context.Context, config ProviderConfig, secret string) error {
	payload := fmt.Sprintf(`{"model":%q,"max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`, config.DefaultModel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(config.BaseURL, "/")+"/v1/messages", strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", secret)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return errors.New("provider connection failed; check the endpoint, network, and credentials")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("provider rejected the API key")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func openAIChatPing(ctx context.Context, config ProviderConfig, secret string) error {
	payload := fmt.Sprintf(`{"model":%q,"max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`, config.DefaultModel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(config.BaseURL, "/")+"/chat/completions", strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("provider rejected the API key")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// listProviderModels returns the model IDs a provider advertises for the given
// configuration. Used by the dashboard's "discover models" action.
func listProviderModels(ctx context.Context, config ProviderConfig, secret string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := validateProviderDestination(ctx, config.BaseURL); err != nil {
		return nil, err
	}
	if config.Protocol == "anthropic" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(config.BaseURL, "/")+"/v1/models", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", secret)
		req.Header.Set("anthropic-version", "2023-06-01")
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
		}
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return nil, err
		}
		models := make([]string, 0, len(payload.Data))
		for _, item := range payload.Data {
			if item.ID != "" {
				models = append(models, item.ID)
			}
		}
		return models, nil
	}
	return fetchOpenAIModels(ctx, config.BaseURL, secret)
}

// fetchOpenAIModels lists model IDs from an OpenAI-compatible GET /models
// endpoint. Local implementation keeps Foreman's security surface self-contained.
func fetchOpenAIModels(ctx context.Context, baseURL, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(payload.Data))
	seen := map[string]bool{}
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	return models, nil
}
