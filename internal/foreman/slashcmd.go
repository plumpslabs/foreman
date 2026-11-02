package foreman

import (
	"strings"
)

// Slash commands.
//
// The composer used to send "/journal" as an ordinary chat message, so the
// model received the literal string and answered about it ("The user sent
// /journal. I have already applied both patches…") instead of doing the work.
// A command is a directive, not prose: it must be recognised before it reaches
// the model, mapped to a concrete instruction (or handled natively), and never
// counted as a normal conversation turn when it is purely a control verb.
//
// This parser is deterministic and offline. Every command resolves to either
//
//   - a native handler (no model call at all), or
//   - a rewritten instruction the agent can act on immediately, which is what
//     the model needed to see in the first place.
//
// Unknown "/foo" text is left untouched: it may be a path, a regex, or a
// deliberate literal, and inventing a meaning for it would be worse than
// passing it through.

// slashCommand is one recognised command.
type slashCommand struct {
	// Name is the command without the leading slash ("journal").
	Name string
	// Args is the remaining text after the command word.
	Args string
}

// knownSlashCommands lists the commands the server understands. The composer
// offers the same set (web/src/pages.jsx CHAT_COMMANDS); keeping the list here
// means the server, not the browser, decides what a command means.
var knownSlashCommands = map[string]bool{
	"journal": true,
	"init":    true,
	"plan":    true,
	"spawn":   true,
	"fix":     true,
	"confirm": true,
	"ask":     true,
	"help":    true,
}

// parseSlashCommand recognises a command only when the message STARTS with it.
// A slash inside a sentence ("look at src/foo/journal") is a path, not a verb.
func parseSlashCommand(content string) (slashCommand, bool) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "/") {
		return slashCommand{}, false
	}
	body := strings.TrimPrefix(trimmed, "/")
	if body == "" {
		return slashCommand{}, false
	}
	name := body
	args := ""
	if i := strings.IndexFunc(body, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	}); i >= 0 {
		name = body[:i]
		args = strings.TrimSpace(body[i+1:])
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if !knownSlashCommands[name] {
		return slashCommand{}, false
	}
	return slashCommand{Name: name, Args: args}, true
}

// slashInstruction rewrites a command into the instruction the agent should
// have received. The rewrite is explicit about the mechanism ("call the memory
// tool with action journal") because the model cannot infer that "/journal"
// maps to a tool from the token alone.
//
// Commands that only make sense with arguments (spawn, fix, ask) fall back to
// a clarifying instruction when the argument is missing, rather than sending a
// bare verb that the model will narrate instead of execute.
func slashInstruction(cmd slashCommand) string {
	switch cmd.Name {
	case "journal":
		return "Write a journal entry for this room's recent work now. Use the memory tool with " +
			"action:journal and record what actually changed: the file, the function, a one-sentence " +
			"action, and any dependency. Record one entry per real change. Do not summarise the " +
			"conversation and do not ask for confirmation — if nothing changed, say so plainly and stop."
	case "init":
		return "Orient on this workspace before doing anything else: read AGENTS.md (or the project " +
			"contract), map the repository layout, and state what you understand the project to be, " +
			"its conventions, and the first concrete task. Keep it short."
	case "plan":
		if cmd.Args == "" {
			return "Break the current request into a tracked plan with ordered steps and dependencies, " +
				"then create the tasks. Ask which request to plan if it is ambiguous."
		}
		return "Break this into a tracked plan with ordered steps and dependencies, then create the tasks: " + cmd.Args
	case "spawn":
		if cmd.Args == "" {
			return "Decide which teammate should take the current work, then delegate it with spawn_agent " +
				"using an exact Team Roster name. State who you chose and why."
		}
		return "Delegate this to the right teammate with spawn_agent (exact Team Roster name), with a " +
			"clear title, file paths, and acceptance criteria: " + cmd.Args
	case "fix":
		if cmd.Args == "" {
			return "Diagnose the latest failing check or reported bug, apply the minimal fix, and verify it " +
				"with the narrowest useful command. Report root cause, fix, and evidence."
		}
		return "Diagnose and fix this with the smallest correct change, then verify it. Report root " +
			"cause, fix, and evidence: " + cmd.Args
	case "confirm":
		return "Before any irreversible action, lay out what will change, what cannot be undone, and the " +
			"alternatives — then ask the human to confirm. Do not proceed until they answer."
	case "ask":
		if cmd.Args == "" {
			return "Ask the human the one question that is actually blocking you, with concrete options, " +
				"then stop and wait."
		}
		return "Ask the human this, with concrete options, then stop and wait: " + cmd.Args
	case "help":
		return "" // handled natively
	}
	return ""
}

// slashHelpText is answered natively so /help is instant and never costs a run.
func slashHelpText() string {
	ordered := []string{"/init", "/plan", "/spawn", "/fix", "/confirm", "/ask", "/journal", "/help"}
	return "Commands: " + strings.Join(ordered, " · ") +
		". Type a command at the start of a message; the server turns it into a concrete instruction " +
		"for the agent instead of passing the literal text through."
}

// slashNativeReply handles commands that need no model call. It returns
// (reply, true) when the message was fully answered without a run.
func (s *Server) slashNativeReply(cmd slashCommand) (string, bool) {
	if cmd.Name == "help" {
		return slashHelpText(), true
	}
	return "", false
}
