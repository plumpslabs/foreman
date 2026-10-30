package foreman

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// avatarStyles is the curated set of DiceBear styles used for agent avatars.
// They are all deterministic given a seed, so the same (style, seed) always
// yields the same SVG and the result can be cached to disk.
var avatarStyles = []string{
	"bottts",
	"bottts-neutral",
	"fun-emoji",
	"pixel-art",
	"notionists",
	"adventurer",
	"thumbs",
	"shapes",
}

// diceBearBaseURL is overridable for tests and self-hosted DiceBear instances.
var diceBearBaseURL = "https://api.dicebear.com/10.x"

// randomAvatarSeed returns a short, filesystem-safe random seed.
func randomAvatarSeed() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand should never fail; fall back to a time-based seed so an
		// agent still gets an avatar rather than an empty one.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// randomAvatarStyle picks a random style from the curated list.
func randomAvatarStyle() string {
	buf := make([]byte, 1)
	if _, err := rand.Read(buf); err != nil {
		return avatarStyles[0]
	}
	return avatarStyles[int(buf[0])%len(avatarStyles)]
}

// styleForSeed derives a stable style from a seed so agents that predate an
// explicit style still get a consistent look without persisting one.
func styleForSeed(seed string) string {
	if seed == "" {
		return avatarStyles[0]
	}
	var sum int
	for _, r := range seed {
		sum += int(r)
	}
	if sum < 0 {
		sum = -sum
	}
	return avatarStyles[sum%len(avatarStyles)]
}

// avatarFileName is the on-disk cache name for an agent avatar. The style and
// seed are part of the name so changing either naturally invalidates the cache.
func avatarFileName(id, style, seed string) string {
	safe := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	return fmt.Sprintf("%s-%s-%s.svg", safe(id), safe(style), safe(seed))
}

// avatarURL builds the DiceBear HTTP API URL for a style+seed.
func avatarURL(style, seed string) string {
	return fmt.Sprintf("%s/%s/svg?seed=%s&size=128",
		strings.TrimRight(diceBearBaseURL, "/"), url.PathEscape(style), url.QueryEscape(seed))
}

// fetchAvatar retrieves an avatar SVG from DiceBear. It is network-bound and
// must not be called while holding the store lock.
func fetchAvatar(style, seed string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, avatarURL(style, seed), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dicebear returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("dicebear returned an empty avatar")
	}
	return data, nil
}

// avatarDirFor returns the directory where avatars are cached. It lives next to
// the global profile so the avatars (like the agents they belong to) are shared
// across projects.
func avatarDirFor(globalPath string) string {
	return filepath.Join(filepath.Dir(globalPath), "avatars")
}

// assignAvatar fills in a missing seed/style. It never overwrites an existing
// value, so a user-chosen (or previously random) avatar is stable.
func assignAvatar(agent *Agent) bool {
	if agent == nil {
		return false
	}
	changed := false
	if strings.TrimSpace(agent.AvatarSeed) == "" {
		agent.AvatarSeed = randomAvatarSeed()
		changed = true
	}
	if strings.TrimSpace(agent.AvatarStyle) == "" {
		agent.AvatarStyle = randomAvatarStyle()
		changed = true
	}
	return changed
}

// ensureAgentAvatars backfills avatar seeds/styles for every known agent
// (library, teams, and templates) so pre-existing data gets avatars too. It
// returns true when anything changed and must be persisted.
func (s *Store) ensureAgentAvatars() bool {
	changed := false
	for i := range s.state.Agents {
		if assignAvatar(&s.state.Agents[i]) {
			changed = true
		}
	}
	for i := range s.state.Teams {
		for j := range s.state.Teams[i].Agents {
			if assignAvatar(&s.state.Teams[i].Agents[j]) {
				changed = true
			}
		}
	}
	for i := range s.state.Templates {
		for j := range s.state.Templates[i].Agents {
			if s.state.Templates[i].Agents[j].ID == "" {
				s.state.Templates[i].Agents[j].ID = uuid.NewString()
				changed = true
			}
			if assignAvatar(&s.state.Templates[i].Agents[j]) {
				changed = true
			}
		}
	}
	return changed
}

// FindAgentAvatar resolves the avatar seed/style for an agent ID, searching the
// library first and then every team's members.
func (s *Store) FindAgentAvatar(agentID string) (style, seed string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.state.Agents {
		if s.state.Agents[i].ID == agentID {
			a := s.state.Agents[i]
			return styleOrDefault(a.AvatarStyle, a.AvatarSeed), a.AvatarSeed, true
		}
	}
	for i := range s.state.Teams {
		for j := range s.state.Teams[i].Agents {
			if s.state.Teams[i].Agents[j].ID == agentID {
				a := s.state.Teams[i].Agents[j]
				return styleOrDefault(a.AvatarStyle, a.AvatarSeed), a.AvatarSeed, true
			}
		}
	}
	for i := range s.state.Templates {
		for j := range s.state.Templates[i].Agents {
			if s.state.Templates[i].Agents[j].ID == agentID {
				a := s.state.Templates[i].Agents[j]
				return styleOrDefault(a.AvatarStyle, a.AvatarSeed), a.AvatarSeed, true
			}
		}
	}
	return "", "", false
}

func styleOrDefault(style, seed string) string {
	if strings.TrimSpace(style) != "" {
		return style
	}
	return styleForSeed(seed)
}

// AvatarSVG returns the cached SVG for an agent, fetching and caching it on a
// miss. A nil slice with a nil error means the avatar is unavailable (for
// example, offline and never cached) and the caller should fall back to
// initials. This is network-bound on a miss, so it does not hold the store lock.
func (s *Store) AvatarSVG(agentID string) ([]byte, error) {
	style, seed, ok := s.FindAgentAvatar(agentID)
	if !ok {
		return nil, nil
	}
	if seed == "" {
		seed = agentID
	}
	path := filepath.Join(s.avatarDir, avatarFileName(agentID, style, seed))
	if data, err := os.ReadFile(path); err == nil {
		return data, nil
	}
	data, err := fetchAvatar(style, seed)
	if err != nil {
		return nil, err
	}
	if s.avatarDir != "" {
		_ = os.MkdirAll(s.avatarDir, 0o755)
		_ = os.WriteFile(path, data, 0o644)
	}
	return data, nil
}

// ShuffleAgentAvatar assigns a fresh random seed (and validates the style),
// persists the change, and returns the updated agent. It is used by the
// "shuffle" action so a user can re-roll an avatar they do not like.
func (s *Store) ShuffleAgentAvatar(agentID string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Agents {
		if s.state.Agents[i].ID == agentID {
			s.state.Agents[i].AvatarSeed = randomAvatarSeed()
			s.state.Agents[i].AvatarStyle = randomAvatarStyle()
			if err := s.save(); err != nil {
				return Agent{}, err
			}
			return s.state.Agents[i], nil
		}
	}
	return Agent{}, fmt.Errorf("unknown agent %q", agentID)
}
