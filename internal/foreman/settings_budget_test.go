package foreman

import "testing"

func TestNormalizeSettingsLoopAndCompactionDefaults(t *testing.T) {
	got := normalizeSettings(Settings{Theme: "dark", EffortMode: "automatic", DefaultEffort: "medium"})
	if got.MaxIterations != 30 {
		t.Errorf("MaxIterations default = %d, want 30", got.MaxIterations)
	}
	if got.ReadOnlyBudget != 40 {
		t.Errorf("ReadOnlyBudget default = %d, want 40", got.ReadOnlyBudget)
	}
	if got.CompactionThreshold != 120000 {
		t.Errorf("CompactionThreshold default = %d, want 120000", got.CompactionThreshold)
	}
	if got.CompactionRetention != 24 {
		t.Errorf("CompactionRetention default = %d, want 24", got.CompactionRetention)
	}
	// Overrides are honored within range.
	got = normalizeSettings(Settings{MaxIterations: 80, ReadOnlyBudget: 10, CompactionThreshold: 5000, CompactionRetention: 5})
	if got.MaxIterations != 80 || got.ReadOnlyBudget != 10 || got.CompactionThreshold != 5000 || got.CompactionRetention != 5 {
		t.Errorf("explicit settings were not preserved: %+v", got)
	}
}
