package foreman

import (
	"fmt"
	"strings"
)

// actionDetector implements the loop guard heuristics (doom repeat, period
// cycles, low-progress) as a pure state machine so it can be unit-tested
// without any LLM.
//
// Signatures are per tool *call* ("tool:args"). A multi-tool round is fed as a
// single observeRound call. Judging each call on its own arguments matters: an
// earlier version folded a whole batch into one argument-less signature, which
// made two different batches (same tool names, different files) look identical
// and could trip a false "repeating cycle" almost immediately.
//
// Heuristics, in order of strength:
//   - doom:      the same tool + same args, 3 calls in a row.
//   - cycle:     the last 2p signatures form a clean period-p loop (p = 2..4).
//   - lowScore:  the recent window uses at most 2 distinct *tools* for many
//     rounds in a row, even when the arguments keep changing (which evades
//     doom/cycle). Counted once per *round* so a batch-heavy agent is judged
//     the same way as a single-call one.
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

// observe ingests one tool-call signature. Kept for callers and tests that
// already work one call at a time; it is a thin wrapper over observeRound.
func (d *actionDetector) observe(sig string) (bool, string) {
	return d.observeRound([]string{sig})
}

// observeRound ingests every tool call made in one model round and reports
// stop/reason. Feeding the whole round at once keeps the window continuous
// across multi-tool rounds instead of wiping it, so churn hidden between
// batches can no longer evade detection.
func (d *actionDetector) observeRound(sigs []string) (bool, string) {
	if len(sigs) == 0 {
		return false, ""
	}
	d.totalRounds++

	for _, sig := range sigs {
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

		// cycle: last 2p signatures form a period-p loop (p = 2..4).
		n := len(d.window)
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
					return true, fmt.Sprintf("Detected a repeating cycle of %d steps without progress.", p)
				}
			}
		}
	}

	// low progress: only 1–2 distinct tools across the window, even if the
	// arguments keep changing. Counted once per round; resets as soon as a
	// third distinct tool appears.
	if len(d.window) >= 8 {
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
