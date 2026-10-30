package foreman

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func runDetector(t *testing.T, sigs []string) (string, int) {
	t.Helper()
	d := &actionDetector{}
	for i, s := range sigs {
		if stop, reason := d.observe(s); stop {
			return reason, i + 1
		}
	}
	return "", len(sigs)
}

func TestDetectorDoomRepeat(t *testing.T) {
	reason, at := runDetector(t, []string{"a", "a", "a"})
	if at != 3 || !strings.Contains(reason, "Repeated identical action") {
		t.Fatalf("expected doom at step 3, got step %d reason=%q", at, reason)
	}
}

func TestDetectorCycle(t *testing.T) {
	reason, at := runDetector(t, []string{"a", "b", "a", "b", "a", "b"})
	if !strings.Contains(reason, "repeating cycle") {
		t.Fatalf("expected cycle stop, got step %d reason=%q", at, reason)
	}
}

// Low progress: same tool, ever-changing args. This evades doom (no identical
// repetition) and cycle (no clean period), so it must be caught by the
// tool-name heuristic.
func TestDetectorLowProgress(t *testing.T) {
	d := &actionDetector{}
	var stop bool
	var reason string
	for i := 0; i < 40; i++ {
		stop, reason = d.observe(fmt.Sprintf("read_file:path/file_%d.go", i))
		if stop {
			break
		}
	}
	if !stop || !strings.Contains(reason, "Low progress") {
		t.Fatalf("expected low-progress stop, got stop=%v reason=%q", stop, reason)
	}
}

// A third distinct tool resets the low-progress counter, so varied work runs
// to completion without a false stop.
func TestDetectorLowProgressResetsOnVariety(t *testing.T) {
	d := &actionDetector{}
	for i := 0; i < 12; i++ {
		if stop, reason := d.observe(fmt.Sprintf("read_file:a_%d", i)); stop {
			t.Fatalf("unexpected stop while warming up: %q", reason)
		}
	}
	// Introduce a third distinct tool name before the threshold trips.
	for i := 0; i < 8; i++ {
		if stop, reason := d.observe(fmt.Sprintf("edit_file:b_%d", i)); stop {
			t.Fatalf("unexpected stop after variety: %q", reason)
		}
	}
}

func TestDetectorBenignExplorationNeverStops(t *testing.T) {
	sigs := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t"}
	if reason, _ := runDetector(t, sigs); reason != "" {
		t.Fatalf("benign exploration should not stop, got %q", reason)
	}
}

// Multi-tool batches must clear the window so parallel work never looks like a
// loop.
func TestDetectorResetClearsState(t *testing.T) {
	d := &actionDetector{}
	d.observe("read_file:a")
	d.observe("read_file:a")
	d.reset()
	if len(d.window) != 0 || d.sameCount != 0 || d.lowProg != 0 {
		t.Fatalf("reset did not clear detector state: %+v", d)
	}
	if stop, _ := d.observe("read_file:a"); stop {
		t.Fatalf("detector stopped immediately after reset")
	}
}

// Property: any run drawn from a small action alphabet (1–2 distinct tools)
// must be stopped by the detector within a bounded number of rounds — i.e. a
// genuine loop cannot run forever.
func TestDetectorPropertySmallAlphabetAlwaysStops(t *testing.T) {
	const cap = 100
	for _, alphabet := range [][]string{
		{"read_file"},
		{"read_file", "edit_file"},
	} {
		for seed := int64(0); seed < 200; seed++ {
			rng := rand.New(rand.NewSource(seed))
			d := &actionDetector{}
			stopped := false
			for i := 0; i < cap; i++ {
				tool := alphabet[rng.Intn(len(alphabet))]
				sig := fmt.Sprintf("%s:arg_%d_%d", tool, seed, i)
				if stop, _ := d.observe(sig); stop {
					stopped = true
					break
				}
			}
			if !stopped {
				t.Fatalf("alphabet %v seed %d: detector never stopped within %d rounds", alphabet, seed, cap)
			}
		}
	}
}

// Property: varied work keeps making progress and must not trip the detector.
func TestDetectorPropertyVariedWorkNeverStops(t *testing.T) {
	tools := []string{"read_file", "edit_file", "grep_files", "repo_map", "git_ops"}
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		d := &actionDetector{}
		for i := 0; i < 150; i++ {
			sig := fmt.Sprintf("%s:arg_%d_%d", tools[rng.Intn(len(tools))], seed, i)
			if stop, reason := d.observe(sig); stop {
				t.Fatalf("seed %d round %d: unexpected stop on varied work: %q", seed, i, reason)
			}
		}
	}
}
