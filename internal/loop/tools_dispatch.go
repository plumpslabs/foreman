package loop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/plumpslabs/bro-code/internal/prompt"
	"github.com/plumpslabs/bro-code/internal/provider"
)

// toolsForMode returns the tool surface exposed to the model for the current
// mode. Structural pruning: read-only modes (PLANNER, MINER) simply DO NOT
// receive the mutating tools — write_file/edit_file/delete_file (and the LSP
// tools that write to disk, lsp_fix/lsp_rename) are never offered, so the
// model cannot propose them, cannot waste rounds on guard messages, and pays
// fewer schema tokens per request. PLANNER additionally drops bash entirely.
// BUILDER gets the full surface. The runtime mode guards stay as a backstop
// (MCP/subagent tools bypass this filter), but the LLM is never tempted by
// tools its mode forbids.
func (e *Engine) toolsForMode(mode string) []provider.ToolDefinition {
	defs := e.tools.Definitions()
	// Tool-description lean (P5): trim verbose schemas in every mode so more of
	// the window is free for real task context. Applied before mode pruning.
	if e.toolDescBudget > 0 {
		lean := make([]provider.ToolDefinition, len(defs))
		for i, d := range defs {
			if len(d.Description) > e.toolDescBudget {
				d.Description = strings.TrimSpace(d.Description[:e.toolDescBudget]) + "…"
			}
			lean[i] = d
		}
		defs = lean
	}
	tier := prompt.ClassifyModelTier(e.model)
	// Heavy experimental / analysis tools that bloat schema and confuse open models:
	heavyTools := map[string]bool{
		"blast_radius":     true,
		"refactor_cluster": true,
		"code_impact":      true,
		"checkpoint":       true,
		"context_recall":   true,
		"review_changes":   true,
		"edit_symbol":      true,
		"code_outline":     true,
	}

	exclude := map[string]bool{
		// Redundant tool aliases permanently hidden from LLM tool definitions across ALL tiers:
		// Canonical replacements:
		//   read_files  -> read_file (supports single file or batch paths)
		//   str_replace -> edit_file (surgical verbatim search and replace)
		//   code_slice  -> read_file (with start_line and end_line) or code_locate
		"read_files":  true,
		"str_replace": true,
		"code_slice":  true,
	}
	if mode == "PLANNER" || mode == "MINER" {
		exclude["write_file"] = true
		exclude["edit_file"] = true
		exclude["delete_file"] = true
		exclude["lsp_fix"] = true
		exclude["lsp_rename"] = true
		if mode == "PLANNER" {
			exclude["bash"] = true
		}
	}
	if tier == "weak" || tier == "medium" {
		for h := range heavyTools {
			exclude[h] = true
		}
	}
	// Dynamically masked tools (suppressed for 1-2 rounds to break autoregressive loops):
	for toolName, rounds := range e.maskedTools {
		if rounds > 0 {
			exclude[toolName] = true
		}
	}

	out := make([]provider.ToolDefinition, 0, len(defs))
	for _, d := range defs {
		if exclude[d.Name] {
			continue
		}
		if e.toolDescBudget > 0 && len(d.Description) > e.toolDescBudget {
			d.Description = strings.TrimSpace(d.Description[:e.toolDescBudget]) + "…"
		}
		out = append(out, d)
	}
	return out
}

// recordExplored keeps a capped, de-duplicated list of files/directories the
// model has touched this turn (read_file, list_dir, grep, glob, bash find).
func (e *Engine) recordExplored(tc provider.ToolCall) {
	var target string
	var m map[string]any
	if json.Unmarshal([]byte(tc.Arguments), &m) == nil {
		switch tc.Name {
		case "read_file", "edit_file", "write_file":
			target, _ = m["path"].(string)
			if tc.Name == "edit_file" || tc.Name == "write_file" {
				e.exploredStalls = 0
			} else if tc.Name == "read_file" {
				var startLine, endLine int
				if s, ok := m["start_line"].(float64); ok {
					startLine = int(s)
				}
				if end, ok := m["end_line"].(float64); ok {
					endLine = int(end)
				}
				if startLine > 0 || endLine > 0 {
					target = fmt.Sprintf("%s:%d-%d", target, startLine, endLine)
					e.exploredStalls = 0 // Range read is targeted chunk navigation, not a stall
				}
			}
		case "list_dir", "grep", "glob":
			target, _ = m["path"].(string)
			if target == "" {
				target, _ = m["pattern"].(string)
			}
		case "bash":
			if cmd, _ := m["command"].(string); strings.TrimSpace(cmd) != "" {
				target = "bash: " + strings.TrimSpace(cmd)
			}
		}
	}
	if target == "" {
		return
	}
	if slices.Contains(e.explored, target) {
		return
	}
	e.explored = append(e.explored, target)
	if len(e.explored) > 12 {
		e.explored = e.explored[len(e.explored)-12:]
	}
}

// wasExplored returns true if the given absolute file path was read or searched
// during this turn. Used by the Off-Task Edit Gate.
func (e *Engine) wasExplored(absPath string) bool {
	for _, entry := range e.explored {
		if strings.HasPrefix(entry, "bash: ") {
			continue
		}
		entryAbs, err := filepath.Abs(entry)
		if err != nil {
			entryAbs = entry
		}
		if entryAbs == absPath {
			return true
		}
		if strings.HasPrefix(absPath, entryAbs+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// exploredPathList returns a compact comma-separated list of explored paths
// for use in Off-Task Edit Gate messages.
func (e *Engine) exploredPathList() string {
	var parts []string
	cwd, _ := os.Getwd()
	for _, entry := range e.explored {
		if strings.HasPrefix(entry, "bash: ") {
			continue
		}
		rel := strings.TrimPrefix(entry, cwd+string(os.PathSeparator))
		parts = append(parts, rel)
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// exploredSummary renders the list of files/directories the model has already
// read or searched, used by the tool-budget reminders and the abort message.
func (e *Engine) exploredSummary() string {
	if len(e.explored) == 0 {
		return ""
	}
	var cleanPaths []string
	cwd, _ := os.Getwd()
	for _, entry := range e.explored {
		if strings.HasPrefix(entry, "bash: ") {
			continue
		}
		rel := entry
		if cwd != "" {
			if r, err := filepath.Rel(cwd, entry); err == nil && !strings.HasPrefix(r, "..") {
				rel = r
			}
		}
		cleanPaths = append(cleanPaths, rel)
	}
	if len(cleanPaths) == 0 {
		return ""
	}
	return "\n\nFiles you have already examined: " + strings.Join(cleanPaths, ", ")
}

// isFileMutationTool reports whether a tool name changes the filesystem
// (write/edit/create/delete). Rounds that execute these are credited as
// productive for the Productive Token Ratio metric.
func isFileMutationTool(name string) bool {
	switch name {
	case "write_file", "edit_file", "create_file", "delete_file":
		return true
	}
	return false
}

// isVerificationTool reports whether a tool is a build/test/shell command
// used to verify work. These are EXEMPT from the tool repeat guard: re-running
// the same test command after an edit (or to confirm a flaky test) is the
// fix-verify cycle, not a loop — the earlier result is stale the moment a
// file changes. Abuse is still bounded by the exploration budget and the
// iteration cap.
func isVerificationTool(name string) bool {
	switch name {
	case "bash", "run_tests":
		return true
	}
	return false
}

func isRepeatToolCall(tc, prev provider.ToolCall) bool {
	if prev.Name == "" {
		return false
	}
	if tc.Name != prev.Name {
		return false
	}
	a := strings.TrimSpace(tc.Arguments)
	b := strings.TrimSpace(prev.Arguments)
	if a == b {
		return true
	}
	var ma, mb map[string]any
	if json.Unmarshal([]byte(a), &ma) == nil && json.Unmarshal([]byte(b), &mb) == nil {
		ja, _ := json.Marshal(ma)
		jb, _ := json.Marshal(mb)
		return string(ja) == string(jb)
	}
	return false
}

func normalizedToolCallKey(tc provider.ToolCall) string {
	a := strings.TrimSpace(tc.Arguments)
	var m map[string]any
	if json.Unmarshal([]byte(a), &m) == nil {
		b, _ := json.Marshal(m)
		return tc.Name + ":" + string(b)
	}
	return tc.Name + ":" + a
}

func bashFamily(argsJSON string) string {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.Command == "" {
		return ""
	}
	cmd := strings.TrimSpace(args.Command)
	if i := strings.IndexAny(cmd, " \t"); i > 0 {
		cmd = cmd[:i]
	}
	return strings.ToLower(cmd)
}

func advanceBashFamily(fam, last string, streak int) (string, int) {
	if fam != "" && fam == last {
		return fam, streak + 1
	}
	return fam, 1
}
