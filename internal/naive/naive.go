// Package naive is the honest-baseline attributor: the way most quick
// cost dashboards actually compute spend. It exists so the value of each
// billing rule in the real engine is a measured number, not a claim.
//
// The naive attributor:
//   - prices every event at the REQUESTED model (failover invisible),
//   - bills cached tokens at the full prompt price (no cache discount),
//   - bills all generated completion tokens (aborted streams over-billed),
//   - ignores retries entirely (failed attempts unbilled),
//   - ignores the batch discount,
//   - uses a single frozen price version (v1) forever.
package naive

import "costlens/internal/event"

// Frozen v1 prices, USD per 1K tokens — all this attributor knows.
var v1 = map[string][2]float64{ // [prompt, completion]
	"atlas-1":      {3.0, 15.0},
	"atlas-1-mini": {0.40, 1.60},
	"atlas-2":      {1.0, 5.0},
}

// Cost returns the naive USD cost of one event.
func Cost(e event.Event) float64 {
	p, ok := v1[e.ModelRequested]
	if !ok {
		return 0
	}
	return (float64(e.PromptTokens)*p[0] + float64(e.CompletionTokens)*p[1]) / 1000.0
}
