// Package pricing is the costlens attribution engine: it turns flat usage
// telemetry into dollars by implementing the billing-rule spec.
//
// BILLING-RULE SPEC (the contract the ground-truth harness validates):
//
//  1. Final attempt, prompt: uncached prompt tokens bill at the served
//     model's prompt price; cached (prefix-cache) tokens bill at
//     prompt price × the model's cached_factor.
//  2. Final attempt, completion: billed at the served model's completion
//     price for tokens actually delivered. If the stream aborted, only
//     TokensReceived bill — tokens generated but never delivered do not.
//  3. Retries double-bill the prompt: each failed attempt bills the full
//     prompt at the REQUESTED model's prompt price, with no cached-token
//     discount and no completion charge.
//  4. Failover: the final attempt bills at the SERVED model's prices;
//     failed attempts (which ran on the primary) bill at the REQUESTED
//     model's prompt price (rule 3).
//  5. Batch: requests flagged Batch get the version's batch_discount
//     applied to the whole request (all attempts).
//  6. Prices are versioned: each event bills under its PriceVersion.
//
// Prices are configuration (prices.json), loaded at runtime — the engine
// never hardcodes a price.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"costlens/internal/event"
)

//go:embed prices.json
var pricesJSON []byte

// ModelPrice is one model's rates under one price version. Prices are
// USD per 1K tokens; CachedFactor is the fraction of the prompt price
// charged for a cached token.
type ModelPrice struct {
	PromptPer1K     float64 `json:"prompt_per_1k"`
	CompletionPer1K float64 `json:"completion_per_1k"`
	CachedFactor    float64 `json:"cached_factor"`
}

// Version is one versioned price configuration.
type Version struct {
	BatchDiscount float64               `json:"batch_discount"`
	Models        map[string]ModelPrice `json:"models"`
}

// Table is the full versioned price configuration.
type Table struct {
	Versions map[string]Version `json:"versions"`
}

// DefaultTable loads the embedded price configuration.
func DefaultTable() (*Table, error) {
	var t Table
	if err := json.Unmarshal(pricesJSON, &t); err != nil {
		return nil, fmt.Errorf("parse embedded prices: %w", err)
	}
	return &t, nil
}

// Engine attributes costs using a price Table.
type Engine struct {
	table *Table
}

// New builds an Engine from a price table.
func New(t *Table) *Engine { return &Engine{table: t} }

// Cost returns the attributed USD cost of one event under the spec.
func (eng *Engine) Cost(e event.Event) (float64, error) {
	ver, ok := eng.table.Versions[e.PriceVersion]
	if !ok {
		return 0, fmt.Errorf("unknown price version %q", e.PriceVersion)
	}
	served, ok := ver.Models[e.ModelServed]
	if !ok {
		return 0, fmt.Errorf("unknown served model %q", e.ModelServed)
	}
	requested, ok := ver.Models[e.ModelRequested]
	if !ok {
		return 0, fmt.Errorf("unknown requested model %q", e.ModelRequested)
	}

	// Rule 2: completion bills only for delivered tokens.
	billedCompletion := e.CompletionTokens
	if e.StreamAborted {
		billedCompletion = e.TokensReceived
	}

	// Rules 1+2+4: final attempt at served-model prices.
	uncached := e.PromptTokens - e.CachedTokens
	final := (float64(uncached)*served.PromptPer1K +
		float64(e.CachedTokens)*served.PromptPer1K*served.CachedFactor +
		float64(billedCompletion)*served.CompletionPer1K) / 1000.0

	// Rule 3+4: failed attempts bill the full prompt at requested prices.
	failed := float64(e.Retries) * float64(e.PromptTokens) * requested.PromptPer1K / 1000.0

	total := final + failed

	// Rule 5: batch discount applies to the whole request.
	if e.Batch {
		total *= ver.BatchDiscount
	}
	return total, nil
}
