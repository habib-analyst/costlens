# costlens

**A cost-per-request FinOps engine whose cost attribution is validated against ground-truth metering.**

Every cost tool reports costs. None of them tells you how *wrong* its own attribution is. costlens is built around the opposite premise: an attribution engine that ships with a ground-truth harness — a workload generator that knows the true cost of every event by construction — and publishes its own measured error rate.

## The novel position

Attribution looks trivial (tokens × price) until the hard cases arrive:

- **Prefix-cached prompts** — cached tokens bill at a fraction of the prompt price; ignore that and you over-attribute by ~29% on cached traffic.
- **Retries that double-bill** — a failed attempt still bills its prompt; ignore retries and you under-attribute retry traffic by ~34%.
- **Streams aborted mid-way** — only delivered tokens bill; bill generated tokens and you over-attribute aborted streams by ~70%.
- **Batch discounts** — batch API requests bill at half price; miss the flag and you over-attribute by ~95%.
- **Failover** — a request served by a cheaper fallback model bills at the *served* model's prices; price it at the requested model and you over-attribute by ~229%.
- **Mid-window price changes** — events bill under the price version in force when they ran.

costlens implements all six rules as an explicit, versioned billing spec — and then *proves* the implementation against an independent accounting of the same spec (see [PAPER.md](PAPER.md)).

## Measured results (seed 42, canonical; `results.json`)

Workload: 37,729 events over 72 simulated hours, 4 tenants, 3 endpoints, true spend $345,103.29.

| Attributor | Total attribution error |
|---|---|
| **costlens engine** | **0.0000%** (worst slice: 2.1×10⁻¹⁴%) |
| Naive attributor (no rules) | 6.59% |

Anomaly detection on rollups, scored against injected labelled anomalies (spend spike, slow bleed, new expensive endpoint; 37 labelled cells):

| Metric | Value |
|---|---|
| Precision | 1.000 (zero false alerts) |
| Recall | 0.486 (spike and new-endpoint caught in the first hour; slow bleed is the hard case — first detected at +19h) |

Full numbers, per-case breakdowns, and robustness across seeds 7/42/123 are in [PAPER.md](PAPER.md) and `results.json`.

## Quickstart

```bash
go build -o costlens ./cmd/costlens

# Generate a workload (ground-truth sidecar written alongside)
./costlens gen -seed 42 -out events.jsonl

# Slice attributed costs from the CLI
./costlens rollup events.jsonl
./costlens rollup -tenant acme -endpoint /v1/rag events.jsonl

# Run the full validation: attribution error + anomaly precision/recall
./costlens validate -seed 42 -out results.json

# Serve: HTTP ingest + rollup query API
./costlens serve -addr :8080
curl -X POST --data-binary @events.jsonl localhost:8080/ingest
curl 'localhost:8080/rollup?tenant=acme&endpoint=/v1/chat'
```

## Architecture

| Package | Role |
|---|---|
| `internal/event` | Usage-event schema (flat gateway telemetry: tenant, endpoint, requested/served model, prompt/completion/cached tokens, tokens received, retries, aborted/batch flags, price version) + JSONL reader |
| `internal/pricing` | The attribution engine: versioned price table (embedded `prices.json`) + the six billing rules |
| `internal/rollup` | Per-tenant / endpoint / model / hour rollups with slice queries and hourly series |
| `internal/truth` | The ground-truth harness: seeded workload generator that simulates individual attempts and accounts for them with an **independent implementation** (integer micro-dollar arithmetic, literal price constants, no shared code with the engine) |
| `internal/naive` | The honest baseline: a typical dashboard attributor (requested-model prices, no cache discount, no aborted-stream rule, no retries, no batch, frozen prices) |
| `internal/anomaly` | Rolling median+MAD robust-z detector over hourly series, plus a new-series rule for expensive endpoints born mid-window |
| `internal/eval` | Diffs attributed vs true (total, per slice, per hard case) and scores anomaly alerts cell-by-cell against the injection manifest |

## The independence contract

The validation is only worth anything if the ground truth is not the engine grading its own homework. So `internal/truth` shares **no functions and no price data** with `internal/pricing`: the engine loads float64 prices from JSON config; the harness hardcodes integer micro-dollar constants and bills simulated attempts one by one. If the flat telemetry record could not determine the bill, the diff would show it as residual error. (On this spec it can — the measured residual is float-rounding noise, reported as-is. The naive baseline, run on the identical events, shows what each rule is worth in numbers.)

## Tests

```bash
go test ./...
```

17 tests: every billing rule against hand-computed expectations, rollup math, detector behaviour (spike / flat / new-series / founding-series), and an end-to-end test asserting the engine lands within rounding noise of ground truth while the naive attributor is measurably wrong.

## Honest limits

- Prices and traffic are **synthetic** (the models "atlas-1/2/mini" are stand-ins); the billing *rules* mirror real provider behaviour, the numbers validate the engine, not any real invoice.
- Single-node, in-memory rollups; no persistence, no multi-process ingest.
- The slow-bleed anomaly mostly evades the detector until late (recall is dominated by it) — measured, not hidden.
- Real provider telemetry has genuinely underdetermined cases (e.g. per-attempt cache behaviour under provider-specific retry rules) that would produce nonzero residual error; this spec's telemetry determines the bill exactly, and costlens says so rather than inflating the result.

## License

MIT
