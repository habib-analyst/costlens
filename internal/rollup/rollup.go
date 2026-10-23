// Package rollup aggregates attributed per-event costs into the slices
// FinOps consumers actually ask for: per tenant, per endpoint, per model,
// per hour — and any combination.
package rollup

import (
	"sort"

	"costlens/internal/event"
)

// Key identifies one rollup cell. Empty string / -1 means "any" in a
// Query filter; in stored cells every field is populated.
type Key struct {
	Tenant   string `json:"tenant"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"` // served model
	Hour     int64  `json:"hour"`  // unix hour; -1 = all hours
}

// Cell is one aggregated slice.
type Cell struct {
	Key   Key     `json:"key"`
	Cost  float64 `json:"cost_usd"`
	Count int     `json:"count"`
}

// Store accumulates attributed costs.
type Store struct {
	cells map[Key]*Cell
}

// NewStore returns an empty Store.
func NewStore() *Store { return &Store{cells: make(map[Key]*Cell)} }

// Add folds one event's attributed cost into every rollup granularity:
// the full key and the all-hours key.
func (s *Store) Add(e event.Event, cost float64) {
	for _, hour := range []int64{e.Hour(), -1} {
		k := Key{Tenant: e.Tenant, Endpoint: e.Endpoint, Model: e.ModelServed, Hour: hour}
		c, ok := s.cells[k]
		if !ok {
			c = &Cell{Key: k}
			s.cells[k] = c
		}
		c.Cost += cost
		c.Count++
	}
}

// Query returns cells matching the filter (empty Tenant/Endpoint/Model =
// any; Hour -1 = any), sorted by cost descending.
func (s *Store) Query(f Key) []Cell {
	out := []Cell{}
	for _, c := range s.cells {
		if f.Tenant != "" && c.Key.Tenant != f.Tenant {
			continue
		}
		if f.Endpoint != "" && c.Key.Endpoint != f.Endpoint {
			continue
		}
		if f.Model != "" && c.Key.Model != f.Model {
			continue
		}
		if f.Hour != -1 && c.Key.Hour != f.Hour {
			continue
		}
		// Stored cells exist at hour granularity and all-hours; a filter
		// for a specific hour must not also match the all-hours cell.
		if f.Hour != -1 && c.Key.Hour == -1 {
			continue
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
	return out
}

// Total returns the summed cost over all events added.
func (s *Store) Total() float64 {
	var t float64
	for _, c := range s.cells {
		if c.Key.Hour == -1 {
			t += c.Cost
		}
	}
	return t
}

// HourlySeries returns cost per hour for one tenant×endpoint series,
// for hours [from, to] inclusive, zero-filled.
func (s *Store) HourlySeries(tenant, endpoint string, from, to int64) []float64 {
	out := make([]float64, to-from+1)
	for _, c := range s.cells {
		if c.Key.Tenant == tenant && c.Key.Endpoint == endpoint && c.Key.Hour >= from && c.Key.Hour <= to {
			out[c.Key.Hour-from] += c.Cost
		}
	}
	return out
}

// SeriesKeys lists the distinct tenant×endpoint series present.
func (s *Store) SeriesKeys() [][2]string {
	seen := make(map[[2]string]bool)
	for _, c := range s.cells {
		seen[[2]string{c.Key.Tenant, c.Key.Endpoint}] = true
	}
	out := make([][2]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}
