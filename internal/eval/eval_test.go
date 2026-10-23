package eval

import (
	"testing"

	"costlens/internal/pricing"
	"costlens/internal/truth"
)

func testEngine(t *testing.T) *pricing.Engine {
	t.Helper()
	tab, err := pricing.DefaultTable()
	if err != nil {
		t.Fatal(err)
	}
	return pricing.New(tab)
}

// End-to-end: on a generated workload, the engine's attribution must land
// within float/integer-rounding noise of ground truth, while the naive
// attributor must be measurably wrong. These thresholds are loose bounds
// around the measured values, not the measurements themselves.
func TestEngineMatchesGroundTruth(t *testing.T) {
	w := truth.Generate(7, false)
	rep, err := Evaluate(w, testEngine(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Events == 0 {
		t.Fatal("empty workload")
	}
	if rep.EngineTotalErrPct > 0.05 {
		t.Errorf("engine total error = %.4f%%, want < 0.05%%", rep.EngineTotalErrPct)
	}
	if rep.WorstSlice.AbsErrPct > 0.5 {
		t.Errorf("worst slice %q error = %.4f%%, want < 0.5%%", rep.WorstSlice.Slice, rep.WorstSlice.AbsErrPct)
	}
	if rep.NaiveTotalErrPct < 0.5 {
		t.Errorf("naive total error = %.4f%%, want > 0.5%% (baseline must be measurably wrong)", rep.NaiveTotalErrPct)
	}
	for _, c := range rep.Cases {
		if c.EngineAbsErrPct > 0.5 {
			t.Errorf("case %s engine error = %.4f%%, want < 0.5%%", c.Case, c.EngineAbsErrPct)
		}
	}
}

func TestAnomalyEvalDetectsSpike(t *testing.T) {
	w := truth.Generate(7, true)
	rep, err := EvaluateAnomalies(w, testEngine(t))
	if err != nil {
		t.Fatal(err)
	}
	if rep.TrueCells == 0 {
		t.Fatal("no anomaly cells injected")
	}
	if rep.TimeToDetectHrs["spike"] != 0 {
		t.Errorf("spike time-to-detect = %d, want 0", rep.TimeToDetectHrs["spike"])
	}
	if rep.Recall <= 0 || rep.Precision <= 0 {
		t.Errorf("precision %.3f recall %.3f, want both > 0", rep.Precision, rep.Recall)
	}
}
