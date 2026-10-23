// Package eval diffs attributed costs against ground truth and scores
// anomaly detection against injected, labelled anomalies. Everything it
// reports is measured on the generated workloads — no number in the
// report is written by hand.
package eval

import (
	"fmt"
	"math"
	"sort"

	"costlens/internal/anomaly"
	"costlens/internal/event"
	"costlens/internal/naive"
	"costlens/internal/pricing"
	"costlens/internal/rollup"
	"costlens/internal/truth"
)

// SliceError is the attribution error for one rollup slice.
type SliceError struct {
	Slice         string  `json:"slice"`
	TrueUSD       float64 `json:"true_usd"`
	AttributedUSD float64 `json:"attributed_usd"`
	AbsErrPct     float64 `json:"abs_err_pct"`
}

// CaseError is the attribution error over the subset of events carrying
// one hard-case flag, for the engine and the naive attributor.
type CaseError struct {
	Case            string  `json:"case"`
	Events          int     `json:"events"`
	EngineAbsErrPct float64 `json:"engine_abs_err_pct"`
	NaiveAbsErrPct  float64 `json:"naive_abs_err_pct"`
}

// Report is the full attribution-validation result.
type Report struct {
	Events            int          `json:"events"`
	TrueTotalUSD      float64      `json:"true_total_usd"`
	EngineTotalUSD    float64      `json:"engine_total_usd"`
	EngineTotalErrPct float64      `json:"engine_total_err_pct"`
	NaiveTotalUSD     float64      `json:"naive_total_usd"`
	NaiveTotalErrPct  float64      `json:"naive_total_err_pct"`
	WorstSlice        SliceError   `json:"worst_slice"`
	Slices            []SliceError `json:"slices"`
	Cases             []CaseError  `json:"cases"`
}

// sliceFloorUSD: slices with less true spend than this are excluded from
// the worst-slice ranking (a $0.03 slice can be 40% wrong on one event).
const sliceFloorUSD = 10.0

func pct(attributed, true float64) float64 {
	if true == 0 {
		return 0
	}
	return math.Abs(attributed-true) / true * 100
}

func casesOf(e event.Event) []string {
	var cs []string
	if e.CachedTokens > 0 {
		cs = append(cs, "cached-tokens")
	}
	if e.Retries > 0 {
		cs = append(cs, "retries")
	}
	if e.StreamAborted {
		cs = append(cs, "aborted-stream")
	}
	if e.Batch {
		cs = append(cs, "batch")
	}
	if e.ModelServed != e.ModelRequested {
		cs = append(cs, "failover")
	}
	if e.PriceVersion == "v2" {
		cs = append(cs, "price-v2")
	}
	return cs
}

// Evaluate runs the engine and the naive attributor over the workload and
// diffs both against ground truth, in total, per slice, and per hard case.
func Evaluate(w *truth.Workload, eng *pricing.Engine) (*Report, error) {
	rep := &Report{Events: len(w.Events)}
	slices := map[string]*SliceError{}
	caseTrue := map[string]float64{}
	caseEng := map[string]float64{}
	caseNaive := map[string]float64{}
	caseN := map[string]int{}

	addSlice := func(name string, trueUSD, attributedUSD float64) {
		s, ok := slices[name]
		if !ok {
			s = &SliceError{Slice: name}
			slices[name] = s
		}
		s.TrueUSD += trueUSD
		s.AttributedUSD += attributedUSD
	}

	for _, e := range w.Events {
		trueUSD := float64(w.TrueMicro[e.RequestID]) / 1e6
		engUSD, err := eng.Cost(e)
		if err != nil {
			return nil, fmt.Errorf("engine cost %s: %w", e.RequestID, err)
		}
		naiveUSD := naive.Cost(e)

		rep.TrueTotalUSD += trueUSD
		rep.EngineTotalUSD += engUSD
		rep.NaiveTotalUSD += naiveUSD

		addSlice("tenant="+e.Tenant, trueUSD, engUSD)
		addSlice("endpoint="+e.Endpoint, trueUSD, engUSD)
		addSlice("model="+e.ModelServed, trueUSD, engUSD)
		addSlice("tenant×endpoint="+e.Tenant+" "+e.Endpoint, trueUSD, engUSD)

		for _, c := range casesOf(e) {
			caseTrue[c] += trueUSD
			caseEng[c] += engUSD
			caseNaive[c] += naiveUSD
			caseN[c]++
		}
	}
	rep.EngineTotalErrPct = pct(rep.EngineTotalUSD, rep.TrueTotalUSD)
	rep.NaiveTotalErrPct = pct(rep.NaiveTotalUSD, rep.TrueTotalUSD)

	for _, s := range slices {
		s.AbsErrPct = pct(s.AttributedUSD, s.TrueUSD)
		rep.Slices = append(rep.Slices, *s)
		if s.TrueUSD >= sliceFloorUSD && s.AbsErrPct > rep.WorstSlice.AbsErrPct {
			rep.WorstSlice = *s
		}
	}
	sort.Slice(rep.Slices, func(i, j int) bool { return rep.Slices[i].AbsErrPct > rep.Slices[j].AbsErrPct })

	for c, n := range caseN {
		rep.Cases = append(rep.Cases, CaseError{
			Case:            c,
			Events:          n,
			EngineAbsErrPct: pct(caseEng[c], caseTrue[c]),
			NaiveAbsErrPct:  pct(caseNaive[c], caseTrue[c]),
		})
	}
	sort.Slice(rep.Cases, func(i, j int) bool { return rep.Cases[i].Case < rep.Cases[j].Case })
	return rep, nil
}

// AnomalyReport scores detection against the injected-anomaly manifest.
type AnomalyReport struct {
	TrueCells       int             `json:"true_cells"`
	AlertCells      int             `json:"alert_cells"`
	TruePositives   int             `json:"true_positives"`
	Precision       float64         `json:"precision"`
	Recall          float64         `json:"recall"`
	TimeToDetectHrs map[string]int  `json:"time_to_detect_hours"` // per anomaly kind; -1 = never detected
	Alerts          []anomaly.Alert `json:"alerts"`
}

type cellID struct {
	tenant, endpoint string
	hour             int64
}

// EvaluateAnomalies attributes the anomaly workload with the engine,
// builds hourly rollups, runs the detector, and scores the alerts
// cell-by-cell against the injected manifest.
func EvaluateAnomalies(w *truth.Workload, eng *pricing.Engine) (*AnomalyReport, error) {
	store := rollup.NewStore()
	for _, e := range w.Events {
		c, err := eng.Cost(e)
		if err != nil {
			return nil, fmt.Errorf("engine cost %s: %w", e.RequestID, err)
		}
		store.Add(e, c)
	}
	to := w.StartHour + truth.Hours - 1
	var series []anomaly.Series
	for _, k := range store.SeriesKeys() {
		series = append(series, anomaly.Series{
			Tenant:    k[0],
			Endpoint:  k[1],
			StartHour: w.StartHour,
			Values:    store.HourlySeries(k[0], k[1], w.StartHour, to),
		})
	}
	alerts := anomaly.Detect(series, anomaly.DefaultOptions())

	truthCells := make(map[cellID]string) // cell → kind
	for _, a := range w.Anomalies {
		truthCells[cellID{a.Tenant, a.Endpoint, a.Hour}] = a.Kind
	}
	alertCells := make(map[cellID]bool)
	for _, a := range alerts {
		alertCells[cellID{a.Tenant, a.Endpoint, a.Hour}] = true
	}
	tp := 0
	for c := range alertCells {
		if _, ok := truthCells[c]; ok {
			tp++
		}
	}
	rep := &AnomalyReport{
		TrueCells:       len(truthCells),
		AlertCells:      len(alertCells),
		TruePositives:   tp,
		TimeToDetectHrs: map[string]int{},
		Alerts:          alerts,
	}
	if len(alertCells) > 0 {
		rep.Precision = float64(tp) / float64(len(alertCells))
	}
	if len(truthCells) > 0 {
		rep.Recall = float64(tp) / float64(len(truthCells))
	}
	// Time to detect per kind: first alert inside the kind's cells minus
	// the kind's first injected hour.
	firstInjected := map[string]int64{}
	for c, kind := range truthCells {
		if cur, ok := firstInjected[kind]; !ok || c.hour < cur {
			firstInjected[kind] = c.hour
		}
	}
	for kind, start := range firstInjected {
		ttd := -1
		for _, a := range alerts {
			if truthCells[cellID{a.Tenant, a.Endpoint, a.Hour}] == kind {
				d := int(a.Hour - start)
				if ttd == -1 || d < ttd {
					ttd = d
				}
			}
		}
		rep.TimeToDetectHrs[kind] = ttd
	}
	return rep, nil
}
