package pricing

import (
	"math"
	"testing"

	"costlens/internal/event"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	tab, err := DefaultTable()
	if err != nil {
		t.Fatal(err)
	}
	return New(tab)
}

func base() event.Event {
	return event.Event{
		RequestID: "r1", Tenant: "acme", Endpoint: "/v1/chat",
		ModelRequested: "atlas-1", ModelServed: "atlas-1",
		PromptTokens: 1000, CompletionTokens: 1000, TokensReceived: 1000,
		PriceVersion: "v1",
	}
}

func check(t *testing.T, eng *Engine, e event.Event, want float64) {
	t.Helper()
	got, err := eng.Cost(e)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cost = %.6f, want %.6f", got, want)
	}
}

func TestPlain(t *testing.T) {
	// (1000×3.00 + 1000×15.00)/1000 = 18.00
	check(t, testEngine(t), base(), 18.00)
}

func TestCachedTokensDiscounted(t *testing.T) {
	e := base()
	e.CachedTokens = 800
	e.CompletionTokens, e.TokensReceived = 0, 0
	// (200×3.00 + 800×3.00×0.10)/1000 = (600+240)/1000 = 0.84
	check(t, testEngine(t), e, 0.84)
}

func TestAbortedStreamBillsReceivedOnly(t *testing.T) {
	e := base()
	e.StreamAborted = true
	e.TokensReceived = 250
	// prompt 3.00 + 250×15.00/1000 = 3.00 + 3.75 = 6.75
	check(t, testEngine(t), e, 6.75)
}

func TestNonAbortedStreamIgnoresReceived(t *testing.T) {
	e := base()
	e.TokensReceived = 250 // inconsistent telemetry; not aborted → bill generated
	check(t, testEngine(t), e, 18.00)
}

func TestRetriesDoubleBillPrompt(t *testing.T) {
	e := base()
	e.Retries = 2
	e.CompletionTokens, e.TokensReceived = 500, 500
	// final: 3.00 + 500×15/1000 = 10.50; failed: 2×1000×3.00/1000 = 6.00
	check(t, testEngine(t), e, 16.50)
}

func TestRetryBillsRequestedModelOnFailover(t *testing.T) {
	e := base()
	e.ModelServed = "atlas-2"
	e.Retries = 1
	// final at atlas-2: (1000×1.00 + 1000×5.00)/1000 = 6.00
	// failed attempt at requested atlas-1: 1000×3.00/1000 = 3.00
	check(t, testEngine(t), e, 9.00)
}

func TestBatchDiscount(t *testing.T) {
	e := base()
	e.Batch = true
	check(t, testEngine(t), e, 9.00)
}

func TestPriceVersionV2(t *testing.T) {
	e := base()
	e.PriceVersion = "v2"
	// (1000×3.30 + 1000×16.50)/1000 = 19.80
	check(t, testEngine(t), e, 19.80)
}

func TestUnknownVersionErrors(t *testing.T) {
	e := base()
	e.PriceVersion = "v9"
	if _, err := testEngine(t).Cost(e); err == nil {
		t.Error("expected error for unknown price version")
	}
}
