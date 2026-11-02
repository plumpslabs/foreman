package foreman

import (
	"bufio"
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// detectOpencode runs the local `opencode` CLI (if installed) and returns the
// models it exposes. This is a native auto-detect so users do not have to type
// model names by hand; the result is advisory (the user still picks a base URL
// and credentials).
//
// The CLI lists every model its gateway routes (apinex, beatapi, codebuddy, …),
// which is noise for the OpenCode Zen preset. We keep only entries that belong
// to the Zen gateway itself: the "gateway/opencode/*" routes and any free
// (:free) model, so the picker stays focused on the free Zen models.
// Strictly, only the Zen-free routes are kept (`gateway/opencode/*-free`,
// incl. `:free` variants) — paid Zen routes and other gateways' free models
// are dropped because the preset's base URL only serves Zen models.
func detectOpencode() (bool, []string, error) {
	path, err := exec.LookPath("opencode")
	if err != nil {
		return false, nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "models")
	out, err := cmd.Output()
	if err != nil {
		return true, nil, err
	}
	return true, zenFreeModels(string(out)), nil
}

// zenFreeModels keeps only the free models of the OpenCode Zen gateway from an
// `opencode models` listing and strips the CLI's provider prefix. The CLI
// reports every model as `provider_id/model_id` (e.g. `gateway/opencode/glm-
// 5.3-flash-free`), but the Zen API at https://opencode.ai/zen/v1 expects the
// bare model id (`glm-5.3-flash-free`). Only lines whose provider is the Zen
// gateway (`gateway/opencode/`) AND whose id ends in `-free` or `:free` pass
// the filter; paid Zen routes and free models on other gateways (apinex,
// beatapi, codebuddy, tokenharbor, ...) are dropped. Results are deduped and
// capped at 60 to keep the picker tight.
func zenFreeModels(output string) []string {
	var models []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lower := strings.ToLower(line)
		zenFree := strings.HasPrefix(lower, "gateway/opencode/") &&
			(strings.HasSuffix(lower, "-free") || strings.HasSuffix(lower, ":free"))
		if !zenFree {
			continue
		}
		id := strings.TrimPrefix(line, "gateway/opencode/")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	if len(models) > 60 {
		models = models[:60]
	}
	return models
}

func (s *Server) detectOpencodeHandler(w http.ResponseWriter, r *http.Request) {
	installed, models, err := detectOpencode()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed": installed,
		"models":    models,
		"count":     len(models),
	})
}
