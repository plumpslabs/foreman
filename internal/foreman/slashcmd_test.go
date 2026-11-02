package foreman

import (
	"strings"
	"testing"
)

func TestParseSlashCommandRecognisesLeadingVerb(t *testing.T) {
	cases := []struct {
		in     string
		wantOK bool
		name   string
		args   string
	}{
		{"/journal", true, "journal", ""},
		{"  /journal  ", true, "journal", ""},
		{"/journal ticket-mutation.service.ts", true, "journal", "ticket-mutation.service.ts"},
		{"/plan migrate the statuses", true, "plan", "migrate the statuses"},
		{"/help", true, "help", ""},
		// Not commands: a path or a slash inside prose must pass through.
		{"look at src/foo/journal", false, "", ""},
		{"src/services/tickets/journal", false, "", ""},
		{"what does /journal do?", false, "", ""},
		{"/unknownverb", false, "", ""},
		{"", false, "", ""},
		{"/", false, "", ""},
	}
	for _, c := range cases {
		cmd, ok := parseSlashCommand(c.in)
		if ok != c.wantOK {
			t.Errorf("parseSlashCommand(%q) ok=%v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if cmd.Name != c.name || cmd.Args != c.args {
			t.Errorf("parseSlashCommand(%q) = {%q, %q}, want {%q, %q}", c.in, cmd.Name, cmd.Args, c.name, c.args)
		}
	}
}

func TestSlashJournalBecomesConcreteInstruction(t *testing.T) {
	cmd, ok := parseSlashCommand("/journal")
	if !ok {
		t.Fatal("expected /journal to parse")
	}
	got := slashInstruction(cmd)
	// The old failure mode was that the model received the literal token and
	// narrated it. The rewrite must name the tool and the action.
	for _, want := range []string{"memory", "action:journal", "file"} {
		if !strings.Contains(got, want) {
			t.Errorf("journal instruction missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "The user sent") {
		t.Error("instruction must not describe the command, it must perform it")
	}
}

func TestSlashCommandWithMissingArgsStillGivesADirective(t *testing.T) {
	// "/spawn" with no argument must not produce an empty instruction, or the
	// model is back to narrating a bare verb.
	for _, name := range []string{"spawn", "fix", "ask", "plan"} {
		cmd, ok := parseSlashCommand("/" + name)
		if !ok {
			t.Fatalf("/%s should parse", name)
		}
		if got := slashInstruction(cmd); strings.TrimSpace(got) == "" {
			t.Errorf("/%s produced no instruction", name)
		}
	}
}

func TestSlashCommandArgsArePreserved(t *testing.T) {
	cmd, _ := parseSlashCommand("/fix ticket-mutation.service.ts:737")
	got := slashInstruction(cmd)
	if !strings.Contains(got, "ticket-mutation.service.ts:737") {
		t.Errorf("argument lost in rewrite: %s", got)
	}
}

func TestDetectReplyLanguageIndonesian(t *testing.T) {
	// Real message shape from the room that drifted into English, including a
	// pasted JSON error block (English identifiers must not outvote the prose).
	msg := `bis abantu saya perbiaki seusiakna dmna ad bug gni {
    "success": false,
    "message": "Failed to create ticket",
    "error": "Open status not found"
} dman di staging kekney a status blum tersed coab cek juag seed.td apkah udhsesuai`
	if got := detectReplyLanguage(msg); got != "Indonesian" {
		t.Errorf("detectReplyLanguage = %q, want Indonesian", got)
	}
}

func TestDetectReplyLanguageEnglish(t *testing.T) {
	msg := "please check why the ticket creation fails and fix the seed file for me"
	if got := detectReplyLanguage(msg); got != "English" {
		t.Errorf("detectReplyLanguage = %q, want English", got)
	}
}

func TestDetectReplyLanguageAbstainsOnCodeAndShortText(t *testing.T) {
	// A code paste or a one-word message must NOT produce a confident language:
	// a wrong "reply in X" is worse than the generic contract rule.
	for _, msg := range []string{
		"",
		"ok",
		"grep -rn status backend/src",
		"const x = await tx.select().from(ticketStatuses).where(eq(name, OPEN));",
	} {
		if got := detectReplyLanguage(msg); got != "" {
			t.Errorf("detectReplyLanguage(%q) = %q, want abstain", msg, got)
		}
	}
}

func TestReplyLanguageDirectiveNamesLanguageAndKeepsCode(t *testing.T) {
	got := replyLanguageDirective("Indonesian")
	if !strings.Contains(got, "Indonesian") {
		t.Errorf("directive must name the language: %s", got)
	}
	// The instruction has to say code/identifiers stay untranslated, otherwise
	// models start translating file paths and command names.
	for _, want := range []string{"Code", "identifiers", "never translate"} {
		if !strings.Contains(got, want) {
			t.Errorf("directive missing %q: %s", want, got)
		}
	}
	if replyLanguageDirective("") != "" {
		t.Error("empty language must yield no section")
	}
}

func TestRunPromptStatesReplyLanguageEarly(t *testing.T) {
	room := Room{Name: "fix", Goal: "perbaiki bug status tiket"}
	task := Task{
		Title:       "perbaiki seed status",
		Description: "tolong cek kenapa status open tidak ada dan perbaiki seed-nya ya",
	}
	prompt := buildRunPrompt(room, task, nil, "", []string{"file_editor", "grep"}, false)
	if !strings.Contains(prompt, "Reply language (enforced)") {
		t.Fatalf("prompt missing enforced language section:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Indonesian") {
		t.Error("prompt should name the detected language")
	}
	// It must appear before the long orchestration guidance, not at the end
	// where it gets lost.
	langAt := strings.Index(prompt, "Reply language (enforced)")
	orchAt := strings.Index(prompt, "Orchestration Protocol")
	if orchAt >= 0 && langAt > orchAt {
		t.Error("language directive must precede the orchestration section")
	}
}

func TestCoordinatorGetsDelegationTools(t *testing.T) {
	// The shipped Quick Fix coordinator lists only file_editor+grep+terminal,
	// but its prompt tells it to spawn_agent/create_task. Without this the tool
	// filter strips delegation and the coordinator does the work itself.
	team := Team{
		Name:      "Quick Fix",
		MainAgent: "Quick Coordinator",
		Agents: []Agent{
			{Name: "Quick Coordinator", Role: "Main agent · coordinates", Tools: []string{"file_editor", "grep", "terminal"}},
			{Name: "Implementer", Role: "Worker", Tools: []string{"file_editor", "grep"}},
		},
	}
	task := Task{AgentName: "Quick Coordinator", Title: "fix"}
	runAgent := activeTeamAgent(&team, task)
	if runAgent == nil {
		t.Fatal("expected the coordinator agent")
	}
	role := resolveRoleType(team, task.AgentName, runAgent)
	if role != "coordinator" {
		t.Fatalf("role = %q, want coordinator", role)
	}
	ids := withDelegationTools(agentToolIDs(&team, task))
	allowed := allowedADKToolNames(ids, true)
	for _, want := range []string{"spawn_agent", "create_task", "list_tasks", "update_task", "claim_task"} {
		if !allowed[want] {
			t.Errorf("coordinator is missing %q even though the prompt tells it to delegate", want)
		}
	}
	// The user's own tools must survive the grant.
	for _, want := range []string{"view_file", "grep_files"} {
		if !allowed[want] {
			t.Errorf("coordinator lost its configured tool %q", want)
		}
	}
}

func TestSpecialistDoesNotGainDelegationTools(t *testing.T) {
	// A worker must not silently become a coordinator: spawn authority is a
	// role boundary, and widening it would break the "do not spawn" rule.
	ids := agentToolIDs(&Team{
		MainAgent: "Lead",
		Agents: []Agent{
			{Name: "Lead", Role: "Coordinator", Tools: []string{"task_tracker"}},
			{Name: "Worker", Role: "Worker · implements", Tools: []string{"file_editor", "grep"}},
		},
	}, Task{AgentName: "Worker"})
	allowed := allowedADKToolNames(ids, true)
	if allowed["spawn_agent"] {
		t.Error("a specialist should not be granted spawn_agent by default")
	}
}

func TestSymbolMapIsAlwaysReachable(t *testing.T) {
	// symbol_map is advertised in the prompt as the preferred way to locate a
	// declaration. It used to be missing from the allowlist entirely, so every
	// call the model made was filtered out before reaching the tool set.
	for _, tools := range [][]string{
		{"file_editor"},
		{"grep"},
		{"file_editor", "grep", "terminal"},
	} {
		if !allowedADKToolNames(tools, false)["symbol_map"] {
			t.Errorf("symbol_map unreachable for tools %v", tools)
		}
	}
}

func TestAdvertisedToolsOnlyListsWiredTools(t *testing.T) {
	// Anything the prompt advertises must be permitted, or the model burns a
	// turn calling a tool that does not exist.
	allowed := allowedADKToolNames([]string{"file_editor", "grep"}, false)
	for _, doc := range autonomousToolDocs {
		if allowed[doc.name] {
			continue
		}
		// Core tools are added regardless; only assert on ones we expect wired.
		switch doc.name {
		case "symbol_map", "view_file", "grep_files", "related", "repo_map", "memory":
			t.Errorf("advertised tool %q is not permitted", doc.name)
		}
	}
}
