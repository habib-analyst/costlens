# KILLGATE — costlens

## What already exists

- **OpenCost / Kubecost** — Kubernetes cost allocation from resource requests/usage. Numbers are explicitly "estimates until reconciled" against the cloud bill; idle-cost policy is a configuration choice, not a measured quantity. No per-event ground truth, no published attribution error rate. OpenCost 1.121 added per-token inference metrics (llm-d/vLLM) — still reporting, not validation.
- **Langfuse / Helicone / OpenLIT-based tools (e.g. Tidal Telemetry)** — per-call LLM cost *estimates* from token counts × price tables, shown in traces/dashboards. "Reconciled to your provider invoices" at aggregate level (hourly/monthly totals), which can hide large per-slice errors that cancel out. No hard-case billing spec (cached tokens, retry double-billing, aborted streams, failover pricing) validated against constructed truth.
- **Vantage / CloudZero / Cloudability-style FinOps platforms** — ingest provider billing exports and allocate to teams/features. Attribution quality depends on provider granularity (e.g. Bedrock loses API-key attribution entirely); they distribute whatever the bill says, they do not measure how wrong a streaming attribution engine is.
- **AI gateways (LiteLLM, Bifrost, Cloudflare AI Gateway)** — capture request telemetry and report spend per key/model; cost control is the product, attribution *correctness* is assumed, never measured.

## The gap (searched 2026-10-23)

No available product or open-source tool found that:

1. Ships a **ground-truth harness**: a workload generator where every event's true cost is known by construction, computed by an *independent* accounting implementation.
2. Reports its own **attribution error** (total and worst-slice) against that truth, broken down by the hard billing cases: prefix-cached/shared prompts, retries that double-bill, streams aborted mid-way, batch discounts, failover to a differently-priced model, mid-window price changes.
3. Evaluates its **anomaly detection** with precision/recall against *injected, labelled* anomalies rather than anecdotes.

Cost tools REPORT costs. None VALIDATES its own attribution.

## costlens's novel position

An attribution engine with a measured error rate:

- A versioned pricing engine implementing an explicit billing-rule spec for the hard cases above.
- A ground-truth harness whose accounting is a **separate implementation** of the same spec (integer micro-dollar arithmetic, literal price constants, attempt-level simulation) — the engine only ever sees flat telemetry, so any spec leak in the telemetry shows up as real, measured error.
- A naive attributor (no cached discount, no aborted-stream rule, no retries, no batch, requested-model pricing, single price version) run on the identical workload, so the value of each rule is a number, not a claim.
- Anomaly detection on rollups scored with precision/recall and time-to-detect against injected spend spike, slow bleed, and new-expensive-endpoint anomalies.

## Verdict

**BUILD.** No validated-attribution cost tool exists as an available product. The differentiation is precise and measurable: costlens publishes its own error rate; incumbents publish dashboards.
