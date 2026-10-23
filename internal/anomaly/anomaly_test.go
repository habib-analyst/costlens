package anomaly

import "testing"

func flatSeries(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v + float64(i%3) // tiny deterministic wiggle
	}
	return out
}

func TestSpikeDetected(t *testing.T) {
	vals := flatSeries(72, 100)
	vals[60] = 900
	alerts := Detect([]Series{{Tenant: "globex", Endpoint: "/v1/chat", StartHour: 1000, Values: vals}}, DefaultOptions())
	found := false
	for _, a := range alerts {
		if a.Hour == 1060 && a.Reason == "robust-z" {
			found = true
		}
	}
	if !found {
		t.Errorf("spike at hour 60 not detected; alerts: %v", alerts)
	}
}

func TestFlatSeriesNoAlerts(t *testing.T) {
	vals := flatSeries(72, 100)
	alerts := Detect([]Series{{Tenant: "acme", Endpoint: "/v1/chat", StartHour: 1000, Values: vals}}, DefaultOptions())
	if len(alerts) != 0 {
		t.Errorf("flat series produced %d alerts: %v", len(alerts), alerts)
	}
}

func TestNewExpensiveSeriesDetected(t *testing.T) {
	vals := make([]float64, 72)
	for i := 60; i < 72; i++ {
		vals[i] = 500
	}
	alerts := Detect([]Series{{Tenant: "umbrella", Endpoint: "/v1/fine-tune", StartHour: 1000, Values: vals}}, DefaultOptions())
	if len(alerts) == 0 {
		t.Fatal("new expensive series produced no alerts")
	}
	if alerts[0].Reason != "new-series" || alerts[0].Hour != 1060 {
		t.Errorf("first alert = %+v, want new-series at hour 1060", alerts[0])
	}
}

func TestFoundingSeriesNotFlaggedAsNew(t *testing.T) {
	// A series present (and expensive) from hour 0 is a founding series,
	// not a newborn — the new-series rule must stay quiet.
	vals := flatSeries(72, 500)
	alerts := Detect([]Series{{Tenant: "acme", Endpoint: "/v1/chat", StartHour: 1000, Values: vals}}, DefaultOptions())
	for _, a := range alerts {
		if a.Reason == "new-series" {
			t.Errorf("founding series flagged as new: %+v", a)
		}
	}
}
