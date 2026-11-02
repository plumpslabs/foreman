package foreman

import (
	"fmt"
	"strings"
)

// RunPlan is everything one run needs: the ordered providers that may serve it
// (each with its own ordered credentials and models), plus the flattened model
// chain for display. It exists because a run no longer reduces to "one provider
// + one secret": a provider can hold several accounts, and an agent's model
// chain may name models that live on different providers.
//
// A stage is one endpoint (base URL + protocol) with the credentials and models
// that belong to it. Attempts walk stages in order, so a chain of
// [fast-model@providerA, big-model@providerB] really does fall back across
// providers instead of sending providerB's model id to providerA's base URL.
type RunPlan struct {
	Stages []RunStage
	// ModelChain is every model in the plan, in attempt order, for notices.
	ModelChain []string
}

// RunStage is one provider and the credentials/models usable against it.
type RunStage struct {
	Provider ProviderConfig
	// Keys is the ordered list of usable credentials, first one first. A
	// provider with no named keys yields a single entry named "Default".
	Keys []RunKey
	// Models is the ordered list of models to try on this provider.
	Models []string
}

// RunKey is one candidate credential for a run.
type RunKey struct {
	// Name is the display name of the account ("Default" for the legacy
	// credential, or the user-chosen account name).
	Name string
	// Secret is the raw credential. It is never persisted or logged; errors are
	// redacted against every secret in the plan.
	Secret string
}

// provider returns the stage that serves the first attempt.
func (p RunPlan) provider() ProviderConfig {
	if len(p.Stages) == 0 {
		return ProviderConfig{}
	}
	return p.Stages[0].Provider
}

// keyCount is the number of credentials across every stage.
func (p RunPlan) keyCount() int {
	n := 0
	for _, s := range p.Stages {
		n += len(s.Keys)
	}
	return n
}

// secrets returns every credential in the plan, for redaction.
func (p RunPlan) secrets() []string {
	out := make([]string, 0, p.keyCount())
	for _, s := range p.Stages {
		for _, k := range s.Keys {
			if k.Secret != "" {
				out = append(out, k.Secret)
			}
		}
	}
	return out
}

// normalize guarantees a plan always has at least one attemptable stage, even
// for a local endpoint that needs no credential.
func (p RunPlan) normalize() RunPlan {
	stages := make([]RunStage, 0, len(p.Stages))
	for _, s := range p.Stages {
		if len(s.Keys) == 0 {
			// A keyless endpoint (local model) still gets one attempt.
			s.Keys = []RunKey{{Name: "Default"}}
		}
		if len(s.Models) == 0 {
			if s.Provider.DefaultModel != "" {
				s.Models = []string{s.Provider.DefaultModel}
			}
		}
		if len(s.Models) == 0 {
			continue
		}
		stages = append(stages, s)
	}
	p.Stages = stages
	return p
}

// keyFailureKind classifies a run error for rotation purposes.
type keyFailureKind int

const (
	// failureOther is anything that is not clearly about the credential: model
	// capacity, missing model, timeouts, malformed output. Rotate the model.
	failureOther keyFailureKind = iota
	// failureCredential means the provider rejected the key (401/403). Retire
	// the key and try the next account.
	failureCredential
	// failureQuota means the account is rate-limited or out of quota (429).
	// Try the next account, but keep this one — it may recover later.
	failureQuota
)

// classifyKeyFailure inspects an error to decide whether the credential pool
// should advance. It is intentionally conservative: unknown failures keep the
// existing model-rotation behaviour.
func classifyKeyFailure(err error) keyFailureKind {
	if err == nil {
		return failureOther
	}
	msg := strings.ToLower(err.Error())
	// A malformed response is a sampling glitch: neither the key nor the model
	// is at fault, and the retry loop re-samples before rotating anything.
	if isTransientModelError(err) {
		return failureOther
	}
	if strings.Contains(msg, "401") || strings.Contains(msg, "403") ||
		strings.Contains(msg, "unauthorized") || strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "rejected the api key") || strings.Contains(msg, "invalid api key") ||
		strings.Contains(msg, "invalid_api_key") {
		return failureCredential
	}
	if strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "rate_limit") || strings.Contains(msg, "quota") ||
		strings.Contains(msg, "insufficient") || strings.Contains(msg, "credit") {
		return failureQuota
	}
	return failureOther
}

// rotation walks the plan: which stage (provider), which credential, which
// model each attempt uses, and which axis moves after a failure.
//
// The rule is deliberately narrow:
//   - a credential failure (401/403) retires the key and tries the next account;
//   - a quota failure (429) tries the next account but keeps this one alive;
//   - anything else (503, overload, missing model, malformed output) moves to
//     the next model on the SAME account, because another account on the same
//     overloaded endpoint would fail identically.
//
// When a stage runs out of keys or models, the walk moves to the next stage.
type rotation struct {
	stages []RunStage
	stage  int
	key    int
	model  int
	// dead marks (stage,key) pairs the provider rejected. A rejected key is
	// never retried in the same run.
	dead map[[2]int]bool
	// sampled marks (stage,key,model) positions that already used their one
	// free re-sample after a malformed response.
	sampled map[[3]int]bool
}

func newRotation(plan RunPlan) *rotation {
	plan = plan.normalize()
	return &rotation{stages: plan.Stages, dead: map[[2]int]bool{}, sampled: map[[3]int]bool{}}
}

// resample keeps the current position for one more attempt. It returns false
// when this position already spent its free re-sample, in which case the caller
// should rotate the model instead.
func (r *rotation) resample() bool {
	pos := [3]int{r.stage, r.key, r.model}
	if r.sampled[pos] {
		return false
	}
	r.sampled[pos] = true
	return true
}

// position returns the provider, credential and model for this attempt.
func (r *rotation) position() (RunStage, RunKey, string, bool) {
	if r.stage >= len(r.stages) {
		return RunStage{}, RunKey{}, "", false
	}
	st := r.stages[r.stage]
	if r.key >= len(st.Keys) || r.model >= len(st.Models) {
		return RunStage{}, RunKey{}, "", false
	}
	return st, st.Keys[r.key], st.Models[r.model], true
}

// current returns the stage and model this attempt will use, for notices.
func (r *rotation) current() (RunStage, RunKey, string) {
	st, k, m, _ := r.position()
	return st, k, m
}

// nextKeyInStage moves to the next live credential of the SAME provider.
// A quota failure uses this so it can fall back to rotating the model when the
// provider has only one account.
func (r *rotation) nextKeyInStage() bool {
	if r.stage >= len(r.stages) {
		return false
	}
	for i := r.key + 1; i < len(r.stages[r.stage].Keys); i++ {
		if !r.dead[[2]int{r.stage, i}] {
			r.key = i
			return true
		}
	}
	return false
}

// firstLiveKey returns the index of the first credential in a stage that has
// not been retired, or false when every credential in that stage is dead.
func (r *rotation) firstLiveKey(stage int) (int, bool) {
	if stage < 0 || stage >= len(r.stages) {
		return 0, false
	}
	for k := 0; k < len(r.stages[stage].Keys); k++ {
		if !r.dead[[2]int{stage, k}] {
			return k, true
		}
	}
	return 0, false
}

// rewindToLiveKey returns to the first live credential of the current stage.
//
// It exists for the case where the walk moved forward onto an account the
// provider then rejected. The account it left behind was only rate-limited
// (429), which keeps it alive, so stopping with the neighbour's 401 would tell
// the user the whole provider is dead when it is not. Rewinding is only used
// on a credential failure; a quota failure always moves forward, which is what
// keeps the two from bouncing a run between the same two accounts.
func (r *rotation) rewindToLiveKey() bool {
	if r.stage >= len(r.stages) {
		return false
	}
	k, ok := r.firstLiveKey(r.stage)
	if !ok {
		return false
	}
	r.key = k
	return true
}

// nextModel moves to the next model in the current stage, or to the first
// attempt of the next stage. Reports whether an attempt is available.
//
// The account is reset to the first live credential of the stage: a retired
// credential must never serve a later model, because the provider rejected it
// and another model on the same endpoint will reject it too.
func (r *rotation) nextModel() bool {
	if r.stage >= len(r.stages) {
		return false
	}
	if r.model+1 < len(r.stages[r.stage].Models) {
		if k, ok := r.firstLiveKey(r.stage); ok {
			r.model++
			r.key = k
			return true
		}
	}
	return r.nextStage()
}

// nextStage advances to the first attemptable position of the following stage.
func (r *rotation) nextStage() bool {
	for s := r.stage + 1; s < len(r.stages); s++ {
		st := r.stages[s]
		for k := 0; k < len(st.Keys); k++ {
			if r.dead[[2]int{s, k}] {
				continue
			}
			if len(st.Models) == 0 {
				continue
			}
			r.stage, r.key, r.model = s, k, 0
			return true
		}
	}
	r.stage = len(r.stages)
	return false
}

// killCurrent retires the credential in use (the provider rejected it).
func (r *rotation) killCurrent() {
	if r.stage < len(r.stages) {
		r.dead[[2]int{r.stage, r.key}] = true
	}
}

// allKeysRejected reports whether every credential in every stage was rejected.
func (r *rotation) allKeysRejected() bool {
	total := 0
	for _, s := range r.stages {
		total += len(s.Keys)
	}
	return total > 0 && len(r.dead) >= total
}

// attemptFloor is the minimum number of attempts this plan needs so that no
// account, no model and no stage is left untried: a 3-key provider followed by
// a second provider must not stop before reaching the second endpoint just
// because Max retries is low, and a provider with 4 accounts and 2 models must
// still reach the second model.
//
// Each stage contributes (keys + models - 1) attempts: every credential once,
// plus one attempt per additional model. Counting models matters because the
// per-account floor used to grow with the key count alone, so four or more
// accounts silently made the configured model chain unreachable.
func (r *rotation) attemptFloor() int {
	floor := 0
	for _, s := range r.stages {
		n := len(s.Keys) + len(s.Models) - 1
		if n < 1 {
			n = 1
		}
		floor += n
	}
	if floor < 1 {
		floor = 1
	}
	return floor
}

// errAllKeysRejected is returned when every account for the run's providers
// failed with a credential error, so the run stops instead of burning attempts
// on models that can never be reached.
func errAllKeysRejected(providerName string, count int) error {
	if providerName == "" {
		providerName = "provider"
	}
	if count == 1 {
		return fmt.Errorf("%s rejected the API key — update it in Settings → Providers", providerName)
	}
	return fmt.Errorf("all %d accounts for %s rejected the API key — update a key in Settings → Providers", count, providerName)
}

// errAllProviderKeysRejected covers the cross-provider case: the chain spans
// several endpoints and every one of them refused its key.
func errAllProviderKeysRejected(count int) error {
	return fmt.Errorf("all %d accounts across every provider in this chain rejected the API key — update a key in Settings → Providers", count)
}
