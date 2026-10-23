# costlens: Cost Attribution With a Measured Error Rate

## Abstract

Cost-per-request tooling for LLM workloads — Kubernetes allocators (OpenCost, Kubecost), LLM observability platforms (Langfuse, Helicone), and FinOps suites (Vantage, Cloudability) — reports spend per tenant, endpoint, and model, but none validates its own attribution: numbers are estimates reconciled, at best, against aggregate invoices, where per-slice errors cancel invisibly. We present costlens, an attribution engine built around a **ground-truth harness**: a seeded workload generator that simulates individual provider attempts and accounts for them with an implementation independent of the engine (integer micro-dollar arithmetic, literal price constants, no shared code or price data). On a 37,729-event, 72-hour synthetic workload (true spend $345,103.29), the engine's total attribution error is **0.0000%** with a worst slice error of **2.1×10⁻¹⁴%**, while a naive attributor representative of typical dashboards errs by **6.59%** overall and by **29–229%** on individual hard-case subsets. Anomaly detection over the rollups achieves **precision 1.000 / recall 0.486** against injected, labelled anomalies, with the slow-bleed anomaly identified as the open failure mode.

## 1. The billing-rule spec

Attribution is the application of a billing spec to flat telemetry. The spec costlens implements (mirroring real provider behaviour):

1. **Final attempt, prompt:** uncached prompt tokens bill at the served model's prompt price; cached (prefix-cache) tokens bill at prompt price × the model's cached factor (0.10–0.25 in the price table).
2. **Final attempt, completion:** only tokens *delivered* to the client bill. A stream aborted mid-way bills tokens received, not tokens generated.
3. **Retries:** each failed attempt bills the full prompt at the *requested* model's prompt price — no cached discount, no completion charge. Retries double-bill; an attributor that ignores them under-bills.
4. **Failover:** the final attempt bills at the *served* model's prices; failed attempts bill at the requested model's (rule 3).
5. **Batch:** batch-flagged requests bill at the version's batch discount (0.5×) applied to the whole request.
6. **Versioning:** each event bills under the price version in force at its timestamp; the workload switches v1 → v2 at hour 48 (atlas-1 +10%, atlas-2 −10%).

The telemetry record (per event: tenant, endpoint, requested/served model, prompt/cached/completion tokens, tokens received, retry count, aborted/batch flags, price version) is exactly what a gateway log provides — no per-attempt breakdown.

## 2. Method

**Ground-truth harness** (`internal/truth`). A seeded generator emits traffic for 4 tenants × 3 endpoints over 72 hours with per-series rates, a diurnal shape, and hard cases at realistic frequencies: cached tokens on 8,993 events, retries on 1,503, aborted streams on 1,091, batch on 1,679, failover on 454, price-v2 on 12,481 (seed 42 counts). For each request the generator simulates the attempt sequence (failed attempts on the primary, a final attempt possibly failed over) and computes the true bill from the attempts with its own accounting: literal micro-dollar price constants, per-component integer arithmetic, batch halving by integer ratio. The engine never sees the attempts — only the flattened event.

**Attributors.** The engine (`internal/pricing`) loads a versioned JSON price table and applies the six rules in float64. The naive baseline (`internal/naive`) prices every event at the requested model under frozen v1 prices, bills cached tokens in full, bills all generated completion tokens, and ignores retries and the batch flag — the arithmetic of a typical quick dashboard.

**Scoring.** Errors are absolute relative errors of attributed vs true cost, aggregated (a) in total, (b) per slice (tenant, endpoint, served model, tenant×endpoint; worst-slice ranking excludes slices under $10 true spend), and (c) per hard-case subset (an event belongs to every case its flags trigger).

**Anomaly evaluation.** A second run of the generator injects three labelled anomalies into a copy of the workload: a one-hour **spike** (globex /v1/chat, hour 60, 9× volume), a **slow bleed** (initech /v1/rag, hours 48–71, +5%/hour compounding), and a **new expensive endpoint** (umbrella /v1/fine-tune, hours 60–71, ~$540/hour from birth) — 37 labelled (tenant, endpoint, hour) cells. The detector (rolling 48h median + MAD robust z > 6 with a 1.5× ratio floor, plus a new-series rule for mid-window-born series spending ≥ $100/hour) runs over engine-attributed hourly rollups; alerts are scored cell-by-cell.

## 3. Results (seed 42; canonical `results.json`)

### 3.1 Attribution error

| | Total error | Worst slice |
|---|---|---|
| costlens engine | **0.0000%** | 2.1×10⁻¹⁴% (tenant×endpoint = umbrella /v1/summarize) |
| naive attributor | **6.59%** | — |

Per hard-case subset (absolute error within the subset):

| Case | Events | Engine | Naive |
|---|---|---|---|
| failover | 454 | 0.0000% | **228.89%** |
| batch | 1,679 | 0.0000% | **94.80%** |
| aborted-stream | 1,091 | 0.0000% | **70.12%** |
| retries | 1,503 | 0.0000% | **34.38%** |
| cached-tokens | 8,993 | 0.0000% | **28.53%** |
| price-v2 | 12,481 | 0.0000% | 0.39% |

The engine's residual is float64-vs-integer rounding only: under this spec the flat telemetry determines the bill exactly, and the harness *proves* the engine implements the spec rather than assuming it. The naive numbers are the value of each rule, measured: failover pricing alone is worth a 3.3× correction on failover traffic; the batch flag, a 2× correction.

### 3.2 Anomaly detection

| Anomaly | Labelled cells | Detected | Time to detect |
|---|---|---|---|
| spike (hour 60) | 1 | 1 | 0 h |
| new-endpoint (hours 60–71) | 12 | 12 | 0 h |
| slow-bleed (hours 48–71) | 24 | 5 | 19 h |
| **Overall** | **37** | **18** | precision **1.000**, recall **0.486** |

Zero false alerts across all 876 series-hour cells. The detector never cries wolf; its weakness is patience — a +5%/hour bleed stays inside the robust baseline (which itself adapts as trailing hours absorb the bleed) until the series is ~2.6× its pre-bleed level.

### 3.3 Robustness across seeds

| Seed | Engine total err | Naive total err | Precision | Recall | Bleed TTD |
|---|---|---|---|---|---|
| 42 | 0.0000% | 6.59% | 1.000 | 0.486 | 19 h |
| 7 | 0.0000% | 6.56% | 1.000 | 0.486 | 15 h |
| 123 | 0.0000% | 6.69% | 1.000 | 0.378 | 22 h |

## 4. Discussion and honest limits

- **The zero is a spec-completeness result, not magic.** Given telemetry that determines the bill, a correct implementation has zero error; the contribution is the harness that *demonstrates* correctness per rule and per slice, and quantifies each rule's value via the naive contrast. Real deployments face underdetermined telemetry (provider-specific retry/cache interactions not visible in gateway logs), which would surface here as nonzero residual error — the harness is the instrument that would measure it.
- **Slow bleed is the open problem.** A detector with an adapting baseline structurally lags compounding growth. Non-adapting references (e.g. week-over-week anchored baselines) or cumulative-sum (CUSUM) detectors are the natural next step; the labelled harness makes any improvement measurable.
- **Synthetic everything.** Prices, tenants, and traffic shapes are invented; models are stand-ins. The billing rules mirror documented provider behaviour, but no real invoice was validated.
- **Single node, in-memory.** Rollups live in process memory; nothing about distributed ingest is claimed.

## 5. Reproduce

```bash
go test ./...                       # 17 tests, incl. end-to-end ground-truth diff
go run ./cmd/costlens validate -seed 42 -out results.json
go run ./cmd/costlens gen -seed 42 -out events.jsonl   # + events.truth.json sidecar
```
