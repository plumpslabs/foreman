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
	"github.com/plumpslabs/bro-code/internal/provider"
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
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return errors.New("plain HTTP is allowed only for a local provider endpoint")
	}
	return nil
}

func validateProviderDestination(ctx context.Context, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid provider URL")
	}
	host := strings.ToLower(u.Hostname())
	if isLoopbackHost(host) {
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return errors.New("provider endpoints must not target private or local network hosts")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve provider host: %w", err)
	}
	if len(ips) == 0 {
		return errors.New("provider host did not resolve")
	}
	for _, addr := range ips {
		if !addr.IP.IsGlobalUnicast() || addr.IP.IsPrivate() || addr.IP.IsLoopback() || addr.IP.IsLinkLocalUnicast() {
			return errors.New("provider endpoints must not resolve to private or local network addresses")
		}
	}
	return nil
}

func (s *Store) saveProviderCredential(projectPath, providerID, secret string) error {
	if err := os.MkdirAll(filepath.Join(projectPath, ".foreman"), 0700); err != nil {
		return err
	}
	path := filepath.Join(projectPath, ".foreman", "credentials.json")
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

func (s *Store) loadProviderCredential(projectPath, providerID string) (string, error) {
	path := filepath.Join(projectPath, ".foreman", "credentials.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
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
	}
	s.SetProviderSecretFlag(config.ID, secret != "")
	config.HasAPIKey = secret != ""
	if config.ID == "" {
		config.ID = uuid.NewString()
	}
	return config, nil
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
		u := strings.TrimRight(config.BaseURL, "/") + "/v1/messages"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(`{"model":"`+strings.ReplaceAll(config.DefaultModel, `"`, `\"`)+`","max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`))
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
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
		}
		return nil
	}
	models, err := provider.FetchOpenAIModels(config.BaseURL, secret)
	if err != nil {
		return errors.New("provider connection failed; check the endpoint, network, and credentials")
	}
	for _, model := range models {
		if model == config.DefaultModel {
			return nil
		}
	}
	return fmt.Errorf("connection succeeded, but model %q was not returned by /models", config.DefaultModel)
}
