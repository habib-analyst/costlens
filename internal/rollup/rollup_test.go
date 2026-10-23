package rollup

import (
	"math"
	"testing"

	"costlens/internal/event"
)

func ev(ts int64, tenant, endpoint, model string) event.Event {
	return event.Event{TS: ts, Tenant: tenant, Endpoint: endpoint, ModelServed: model}
}

func TestTotalsAndQuery(t *testing.T) {
	s := NewStore()
	h := int64(3600 * 100)
	s.Add(ev(h+10, "acme", "/v1/chat", "atlas-1"), 2.50)
	s.Add(ev(h+20, "acme", "/v1/chat", "atlas-1"), 1.50)
	s.Add(ev(h+30, "globex", "/v1/chat", "atlas-1"), 4.00)
	if got := s.Total(); math.Abs(got-8.00) > 1e-9 {
		t.Errorf("total = %.4f, want 8.00", got)
	}
	cells := s.Query(Key{Tenant: "acme", Hour: -1})
	found := false
	for _, c := range cells {
		if c.Key.Hour == -1 && c.Key.Endpoint == "/v1/chat" {
			found = true
			if math.Abs(c.Cost-4.00) > 1e-9 || c.Count != 2 {
				t.Errorf("acme cell = $%.2f ×%d, want $4.00 ×2", c.Cost, c.Count)
			}
		}
	}
	if !found {
		t.Error("acme all-hours cell missing")
	}
	// Hour-specific query must not leak the all-hours aggregate.
	hourCells := s.Query(Key{Tenant: "acme", Hour: h / 3600})
	for _, c := range hourCells {
		if c.Key.Hour == -1 {
			t.Error("hour query returned all-hours cell")
		}
	}
}

func TestHourlySeries(t *testing.T) {
	s := NewStore()
	base := int64(3600 * 50)
	s.Add(ev(base+1, "acme", "/v1/rag", "atlas-1"), 3)
	s.Add(ev(base+3600+1, "acme", "/v1/rag", "atlas-1"), 5)
	series := s.HourlySeries("acme", "/v1/rag", 50, 52)
	if len(series) != 3 || series[0] != 3 || series[1] != 5 || series[2] != 0 {
		t.Errorf("series = %v, want [3 5 0]", series)
	}
}
