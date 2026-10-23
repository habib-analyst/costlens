// Package truth is the ground-truth harness: the measured core of
// costlens. It generates a seeded workload of LLM usage events AND
// records the true cost of every event by construction.
//
// INDEPENDENCE CONTRACT: the accounting here is a separate implementation
// of the billing-rule spec from internal/pricing. It shares no functions
// and no price data with the engine: prices below are literal constants
// in integer micro-dollars, the arithmetic is per-attempt integer math,
// and the generator simulates individual attempts (including failed ones)
// before flattening them into the telemetry record the engine sees. If
// the flat record cannot determine the bill, the diff shows it — that is
// the point of the validation.
//
// Provider billing reality implemented here (the spec):
//   - failed attempts bill the full prompt at that attempt's model, no
//     cached discount, no completion;
//   - the final attempt bills uncached prompt at full price, cached
//     prompt at the model's cached rate, and completion only for tokens
//     delivered to the client (an aborted stream bills tokens received,
//     not tokens generated);
//   - batch requests bill at half the summed attempt cost;
//   - prices change at the v1→v2 boundary mid-workload.
package truth

import (
	"fmt"
	"math/rand"
	"sort"

	"costlens/internal/event"
)

// ---- independent price accounting (micro-USD per 1K tokens) ----

type priceSet struct {
	promptMicro     int64
	completionMicro int64
	cachedBP        int64 // cached-token price as basis points of prompt price (1000 = 10%)
}

func priceFor(version, model string) (priceSet, bool) {
	switch version {
	case "v1":
		switch model {
		case "atlas-1":
			return priceSet{3_000_000, 15_000_000, 1000}, true
		case "atlas-1-mini":
			return priceSet{400_000, 1_600_000, 1000}, true
		case "atlas-2":
			return priceSet{1_000_000, 5_000_000, 2500}, true
		}
	case "v2":
		switch model {
		case "atlas-1":
			return priceSet{3_300_000, 16_500_000, 1000}, true
		case "atlas-1-mini":
			return priceSet{400_000, 1_600_000, 1000}, true
		case "atlas-2":
			return priceSet{900_000, 4_500_000, 2500}, true
		}
	}
	return priceSet{}, false
}

// attempt is one simulated provider attempt at a request.
type attempt struct {
	model     string
	prompt    int
	cached    int
	generated int // completion tokens generated
	delivered int // completion tokens delivered to the client
	failed    bool
}

// billAttempts computes the true bill for one request from its simulated
// attempts, in micro-USD. This is the ground truth.
func billAttempts(attempts []attempt, version string, batch bool) int64 {
	var micro int64
	for _, a := range attempts {
		p, ok := priceFor(version, a.model)
		if !ok {
			continue
		}
		if a.failed {
			// Full prompt, no cache discount, no completion.
			micro += int64(a.prompt) * p.promptMicro / 1000
			continue
		}
		uncached := int64(a.prompt - a.cached)
		micro += uncached * p.promptMicro / 1000
		micro += int64(a.cached) * p.promptMicro * p.cachedBP / (1000 * 10000)
		micro += int64(a.delivered) * p.completionMicro / 1000
	}
	if batch {
		micro = micro * 5000 / 10000
	}
	return micro
}

// ---- workload generation ----

// Hours is the workload length.
const Hours = 72

// versionBoundaryHour is when prices switch v1 → v2.
const versionBoundaryHour = 48

// StartTS is the workload start (2026-01-05T00:00:00Z).
const StartTS = 1767571200

// AnomalyCell labels one injected-anomaly hour in one series.
type AnomalyCell struct {
	Tenant   string `json:"tenant"`
	Endpoint string `json:"endpoint"`
	Hour     int64  `json:"hour"` // unix hour bucket
	Kind     string `json:"kind"` // spike | slow-bleed | new-endpoint
}

// Workload is a generated event stream plus its ground truth.
type Workload struct {
	Events    []event.Event
	TrueMicro map[string]int64 // request_id → true cost, micro-USD
	Anomalies []AnomalyCell    // empty unless generated with anomalies
	StartHour int64
}

type seriesPlan struct {
	endpoint  string
	meanPerHr float64
}

var tenants = []struct {
	name  string
	scale float64
}{
	{"acme", 1.0},
	{"globex", 0.8},
	{"initech", 0.6},
	{"umbrella", 0.45},
}

var seriesPlans = []seriesPlan{
	{"/v1/chat", 90},
	{"/v1/rag", 45},
	{"/v1/summarize", 30},
}

// Generate builds a workload. If withAnomalies is set, a labelled copy of
// the anomaly programme (spike, slow bleed, new expensive endpoint) is
// injected and the affected cells are recorded in Workload.Anomalies.
func Generate(seed int64, withAnomalies bool) *Workload {
	rng := rand.New(rand.NewSource(seed))
	w := &Workload{TrueMicro: make(map[string]int64), StartHour: StartTS / 3600}
	seq := 0

	emit := func(ts int64, tenant, endpoint string, synth func() (attempts []attempt, batch bool, requested string)) {
		attempts, batch, requested := synth()
		hour := ts / 3600
		version := "v1"
		if hour-w.StartHour >= versionBoundaryHour {
			version = "v2"
		}
		seq++
		id := fmt.Sprintf("req-%07d", seq)
		final := attempts[len(attempts)-1]
		aborted := final.delivered < final.generated
		ev := event.Event{
			RequestID:        id,
			TS:               ts,
			Tenant:           tenant,
			Endpoint:         endpoint,
			ModelRequested:   requested,
			ModelServed:      final.model,
			PromptTokens:     final.prompt,
			CachedTokens:     final.cached,
			CompletionTokens: final.generated,
			TokensReceived:   final.delivered,
			Retries:          len(attempts) - 1,
			StreamAborted:    aborted,
			Batch:            batch,
			PriceVersion:     version,
		}
		w.Events = append(w.Events, ev)
		w.TrueMicro[id] = billAttempts(attempts, version, batch)
	}

	synthFor := func(endpoint string) func() ([]attempt, bool, string) {
		return func() ([]attempt, bool, string) { return synthRequest(rng, endpoint) }
	}

	for h := int64(0); h < Hours; h++ {
		hourStart := StartTS + h*3600
		diurnal := 0.75 + 0.25*float64((h%24)/6) // mild day shape, deterministic
		for _, t := range tenants {
			for _, sp := range seriesPlans {
				n := int(sp.meanPerHr * t.scale * diurnal * (0.8 + 0.4*rng.Float64()))
				for i := 0; i < n; i++ {
					ts := hourStart + int64(rng.Intn(3600))
					emit(ts, t.name, sp.endpoint, synthFor(sp.endpoint))
				}
				// Injected anomaly: slow bleed on initech /v1/rag.
				if withAnomalies && t.name == "initech" && sp.endpoint == "/v1/rag" && h >= 48 {
					extra := int(float64(n) * (pow(1.05, float64(h-47)) - 1))
					for i := 0; i < extra; i++ {
						ts := hourStart + int64(rng.Intn(3600))
						emit(ts, t.name, sp.endpoint, synthFor(sp.endpoint))
					}
					w.Anomalies = append(w.Anomalies, AnomalyCell{t.name, sp.endpoint, hourStart / 3600, "slow-bleed"})
				}
				// Injected anomaly: one-hour spend spike on globex /v1/chat.
				if withAnomalies && t.name == "globex" && sp.endpoint == "/v1/chat" && h == 60 {
					for i := 0; i < n*8; i++ {
						ts := hourStart + int64(rng.Intn(3600))
						emit(ts, t.name, sp.endpoint, synthFor(sp.endpoint))
					}
					w.Anomalies = append(w.Anomalies, AnomalyCell{t.name, sp.endpoint, hourStart / 3600, "spike"})
				}
			}
			// Injected anomaly: brand-new expensive endpoint for umbrella.
			if withAnomalies && t.name == "umbrella" && h >= 60 {
				for i := 0; i < 6; i++ {
					ts := hourStart + int64(rng.Intn(3600))
					emit(ts, t.name, "/v1/fine-tune", func() ([]attempt, bool, string) {
						prompt := 15000 + rng.Intn(10000)
						gen := 1000 + rng.Intn(2000)
						return []attempt{{model: "atlas-1", prompt: prompt, generated: gen, delivered: gen}}, false, "atlas-1"
					})
				}
				w.Anomalies = append(w.Anomalies, AnomalyCell{t.name, "/v1/fine-tune", hourStart / 3600, "new-endpoint"})
			}
		}
	}

	sort.Slice(w.Events, func(i, j int) bool { return w.Events[i].TS < w.Events[j].TS })
	return w
}

func pow(base, e float64) float64 {
	r := 1.0
	for i := 0; i < int(e); i++ {
		r *= base
	}
	return r
}

// synthRequest simulates one request's attempts for an endpoint,
// including the hard cases: prefix-cache hits, retries, aborted streams,
// batch flagging, and failover to a differently-priced model. It returns
// the attempts, the batch flag, and the model the client requested —
// which the attempts alone cannot always tell you (with no failed
// attempts, the single attempt is the serving one, failed over or not).
func synthRequest(rng *rand.Rand, endpoint string) ([]attempt, bool, string) {
	var requested string
	switch endpoint {
	case "/v1/chat":
		if rng.Float64() < 0.6 {
			requested = "atlas-1"
		} else {
			requested = "atlas-1-mini"
		}
	case "/v1/rag":
		requested = "atlas-1"
	default: // /v1/summarize
		requested = "atlas-1-mini"
	}

	var prompt int
	switch endpoint {
	case "/v1/chat":
		prompt = 300 + int(rng.ExpFloat64()*900)
	case "/v1/rag":
		prompt = 1500 + int(rng.ExpFloat64()*2200)
	default:
		prompt = 800 + int(rng.ExpFloat64()*1200)
	}

	cached := 0
	cacheP, fracLo, fracHi := 0.15, 0.2, 0.5
	if endpoint == "/v1/rag" {
		cacheP, fracLo, fracHi = 0.55, 0.3, 0.8
	}
	if endpoint == "/v1/summarize" {
		cacheP = 0.05
	}
	if rng.Float64() < cacheP {
		cached = int(float64(prompt) * (fracLo + rng.Float64()*(fracHi-fracLo)))
	}

	var generated int
	switch endpoint {
	case "/v1/chat":
		generated = 40 + rng.Intn(1160)
	case "/v1/rag":
		generated = 150 + rng.Intn(650)
	default:
		generated = 100 + rng.Intn(300)
	}

	delivered := generated
	if rng.Float64() < 0.03 { // stream aborted mid-way
		delivered = int(float64(generated) * (0.1 + 0.5*rng.Float64()))
	}

	// Failover: 2% of atlas-1 requests are served by atlas-2 instead.
	served := requested
	if requested == "atlas-1" && rng.Float64() < 0.02 {
		served = "atlas-2"
	}

	// Retries: 4% of requests fail 1–2 times first (on the primary).
	retries := 0
	if rng.Float64() < 0.04 {
		retries = 1
		if rng.Float64() < 0.3 {
			retries = 2
		}
	}
	attempts := make([]attempt, 0, retries+1)
	for i := 0; i < retries; i++ {
		attempts = append(attempts, attempt{
			model:     requested,
			prompt:    prompt,
			generated: rng.Intn(50), // partial generation, never billed
			delivered: 0,
			failed:    true,
		})
	}
	attempts = append(attempts, attempt{
		model:     served,
		prompt:    prompt,
		cached:    cached,
		generated: generated,
		delivered: delivered,
	})

	batch := endpoint == "/v1/summarize" && rng.Float64() < 0.25
	return attempts, batch, requested
}
