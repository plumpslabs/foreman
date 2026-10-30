package foreman

import (
	"fmt"
	"sort"
	"strings"
)

// batchSignature builds a stable signature for a multi-tool round (sorted tool
// names, no args) so a batch still participates in the cycle/low-progress
// heuristics instead of resetting the detector.
func batchSignature(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return "batch:" + strings.Join(sorted, "+")
}

// actionDetector implements the loop guard heuristics (doom repeat, period
// cycles, low-progress) as a pure state machine so it can be unit-tested
// without any LLM. Call observe with the signature of each single-tool round.
//
// Heuristics, in order of strength:
//   - doom:      the same tool + same args, 3 rounds in a row.
//   - cycle:     the last 2p signatures form a clean period-p loop (p = 2..4).
//   - lowScore:  1–2 distinct *tools* used for many rounds in a row, even when
//     the arguments keep changing (which evades doom/cycle). This is the
//     "spinning on the same tool" case.
type actionDetector struct {
	window      []string // last tool signatures, bounded to detectorWindowSize
	lastSig     string
	sameCount   int
	lowProg     int
	totalRounds int
}

const (
	detectorWindowSize = 20
	detectorSameLimit  = 3
	detectorLowSteps   = 14
)

// toolName returns the tool part of a "tool:args" signature.
func toolName(sig string) string {
	if i := strings.IndexByte(sig, ':'); i >= 0 {
		return sig[:i]
	}
	return sig
}

// observe ingests one tool-round signature and reports stop/reason.
func (d *actionDetector) observe(sig string) (bool, string) {
	d.totalRounds++

	// Maintain the recent window first so every heuristic sees the full
	// history, including the round that just happened.
	d.window = append(d.window, sig)
	if len(d.window) > detectorWindowSize {
		d.window = d.window[len(d.window)-detectorWindowSize:]
	}

	// doom: same action repeated back-to-back.
	if sig == d.lastSig {
		d.sameCount++
		if d.sameCount >= detectorSameLimit {
			return true, fmt.Sprintf("Repeated identical action %q %d× without progress — likely stuck.", toolName(sig), detectorSameLimit)
		}
	} else {
		d.lastSig = sig
		d.sameCount = 1
	}

	n := len(d.window)
	// cycle: last 2p signatures form a period-p loop (p = 2..4).
	if n >= 4 {
		for p := 2; p <= 4 && p*2 <= n; p++ {
			ok := true
			for i := n - p*2; i < n-p; i++ {
				if d.window[i] != d.window[i+p] {
					ok = false
					break
				}
			}
			if ok {
				return true, fmt.Sprintf("Detected a repeating cycle of %d steps (%q) without progress.", p, toolName(sig))
			}
		}
	}
	// low progress: only 1–2 distinct tools over many rounds, even if the
	// arguments keep changing. Resets as soon as a third tool appears.
	if n >= 8 {
		tools := map[string]bool{}
		for _, s := range d.window {
			tools[toolName(s)] = true
		}
		if len(tools) <= 2 {
			d.lowProg++
			if d.lowProg >= detectorLowSteps {
				return true, "Low progress: repeating the same 1–2 tools over many steps; stopping with a report instead of looping."
			}
		} else {
			d.lowProg = 0
		}
	}
	return false, ""
}

// reset clears state when a multi-tool batch is seen.
func (d *actionDetector) reset() {
	d.window = nil
	d.lastSig = ""
	d.sameCount = 0
	d.lowProg = 0
}
