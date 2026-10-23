// Package event defines the usage-event schema costlens ingests.
//
// The shape mirrors what an LLM gateway / provider usage log actually
// exposes: flat per-request telemetry. Deliberately, it does NOT carry a
// per-attempt breakdown — attribution has to reconstruct the bill from
// these fields and the billing-rule spec alone. Anything the flat record
// cannot determine shows up as measured attribution error.
package event

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

// Event is one billable LLM request as seen by telemetry.
type Event struct {
	RequestID        string `json:"request_id"`
	TS               int64  `json:"ts"` // unix seconds
	Tenant           string `json:"tenant"`
	Endpoint         string `json:"endpoint"`
	ModelRequested   string `json:"model_requested"`
	ModelServed      string `json:"model_served"` // differs from requested on failover
	PromptTokens     int    `json:"prompt_tokens"`
	CachedTokens     int    `json:"cached_tokens"`     // prompt tokens served from prefix cache (final attempt)
	CompletionTokens int    `json:"completion_tokens"` // tokens generated on the final attempt
	TokensReceived   int    `json:"tokens_received"`   // tokens delivered to the client (== CompletionTokens unless the stream aborted)
	Retries          int    `json:"retries"`           // failed attempts before the final one
	StreamAborted    bool   `json:"stream_aborted"`
	Batch            bool   `json:"batch"` // async batch API (discounted)
	PriceVersion     string `json:"price_version"`
}

// Hour returns the event's hour bucket (unix hours).
func (e Event) Hour() int64 { return e.TS / 3600 }

// ReadJSONL decodes a stream of one-JSON-object-per-line events.
func ReadJSONL(r io.Reader) ([]Event, error) {
	var out []Event
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
