package foreman

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// projectContractFiles are the well-known project-agent-contract files Foreman
// recognizes at the workspace root. AGENTS.md is the emerging cross-tool
// standard; the others are honored so a repo without AGENTS.md still benefits.
// Detection is native (one Stat per candidate) — no external tooling.
var projectContractFiles = []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md", "README.md"}

// detectProjectContract returns the contents of the first recognized
// project-agent-contract file found at the workspace root, preferring the more
// specific agent contracts (AGENTS/CLAUDE/GEMINI) over the generic README.
func detectProjectContract(root string) (string, string, bool) {
	if root == "" {
		return "", "", false
	}
	// Prefer specific agent contracts first.
	for _, name := range projectContractFiles {
		p := filepath.Join(root, name)
		if data, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return name, string(data), true
		}
	}
	return "", "", false
}

// projectContractSection renders the detected contract for injection into a run
// prompt. The agent gets the project's own rules up front so it does not have
// to rediscover conventions on every cold start.
func projectContractSection(root string) string {
	name, body, ok := detectProjectContract(root)
	if !ok {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Project agent contract (")
	b.WriteString(name)
	b.WriteString(" — native detection)\nThis workspace ships its own rules; follow them. If they conflict with a general instruction here, the project file wins.\n\n")
	// Cap injected body so a huge README does not dominate the prompt.
	const maxContractRunes = 6000
	runes := []rune(body)
	if len(runes) > maxContractRunes {
		body = string(runes[:maxContractRunes]) + "\n…(truncated; read the full file with view_file if needed)"
	}
	b.WriteString(body)
	return b.String()
}

// initProjectFromContract reads the workspace's contract/README and turns it
// into durable memory facts plus a starter task so a brand-new workspace is
// not a blank slate. It is idempotent-ish: facts dedupe by body per room, and
// the task is skipped if a same-titled card already exists.
func initProjectFromContract(store *Store, roomID, root, agentName string) (facts, tasks int, err error) {
	if store == nil || store.DB() == nil {
		return 0, 0, nil
	}
	name, body, ok := detectProjectContract(root)
	if !ok {
		return 0, 0, nil
	}
	db := store.DB()
	// Retain the contract itself as a fact so future runs recall it even if the
	// file is later moved.
	if _, ferr := db.SaveMemoryFact(roomID, agentName, "fact", fmt.Sprintf("Project contract %s: %s", name, firstLines(body, 3))); ferr == nil {
		facts++
	}
	// A starter task so the room has orientation on first open.
	const starterTitle = "Orient: read project contract and outstanding work"
	if _, terr := store.CreateTask(Task{
		RoomID:      roomID,
		Title:       starterTitle,
		Description: fmt.Sprintf("Review %s and the current board; summarize scope and any open questions for the human.", name),
		AgentName:   agentName,
		Status:      "todo",
	}); terr == nil {
		tasks++
	}
	return facts, tasks, nil
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, " | ")
}
