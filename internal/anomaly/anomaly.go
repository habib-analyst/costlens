// Package anomaly detects cost anomalies on hourly tenant×endpoint
// rollup series using a rolling robust baseline (median + MAD), plus a
// new-series rule for expensive endpoints that appear mid-window with no
// baseline at all.
package anomaly

import "sort"

// Alert is one flagged (tenant, endpoint, hour) cell.
type Alert struct {
	Tenant   string  `json:"tenant"`
	Endpoint string  `json:"endpoint"`
	Hour     int64   `json:"hour"`
	Reason   string  `json:"reason"` // robust-z | new-series
	Value    float64 `json:"value_usd"`
	Baseline float64 `json:"baseline_usd"`
}

// Options tunes the detector.
type Options struct {
	HistoryHours       int     // trailing window for the baseline (default 48)
	MinHistory         int     // hours of history required before z-alerts (default 24)
	ZThreshold         float64 // robust z-score threshold (default 6)
	MinRatio           float64 // value must exceed baseline × ratio (default 1.5)
	NewSeriesMinUSD    float64 // hourly spend that makes a newborn series an alert (default 100)
	NewSeriesMaxAgeHrs int     // series younger than this can raise new-series alerts (default 24)
}

// DefaultOptions returns the shipped detector configuration.
func DefaultOptions() Options {
	return Options{
		HistoryHours:       48,
		MinHistory:         24,
		ZThreshold:         6,
		MinRatio:           1.5,
		NewSeriesMinUSD:    100,
		NewSeriesMaxAgeHrs: 24,
	}
}

// Series is one tenant×endpoint hourly cost series. Values[i] is the cost
// in hour StartHour+i.
type Series struct {
	Tenant    string
	Endpoint  string
	StartHour int64
	Values    []float64
}

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func mad(xs []float64, med float64) float64 {
	dev := make([]float64, len(xs))
	for i, x := range xs {
		d := x - med
		if d < 0 {
			d = -d
		}
		dev[i] = d
	}
	return median(dev)
}

// Detect scans every series and returns all alerts.
func Detect(series []Series, opts Options) []Alert {
	var alerts []Alert
	for _, s := range series {
		firstSeen := -1
		for i, v := range s.Values {
			if v > 0 {
				firstSeen = i
				break
			}
		}
		for i, v := range s.Values {
			hour := s.StartHour + int64(i)
			// New-series rule: a series born after the window opened
			// (so it is not just a founding series) that is immediately
			// expensive has no baseline to z-score against — flag it on
			// absolute spend while it is young.
			if firstSeen > 0 && i-firstSeen < opts.NewSeriesMaxAgeHrs && v >= opts.NewSeriesMinUSD {
				alerts = append(alerts, Alert{s.Tenant, s.Endpoint, hour, "new-series", v, 0})
				continue
			}
			lo := i - opts.HistoryHours
			if lo < 0 {
				lo = 0
			}
			hist := s.Values[lo:i]
			if len(hist) < opts.MinHistory {
				continue
			}
			med := median(hist)
			sigma := 1.4826 * mad(hist, med)
			if sigma <= 0 {
				continue
			}
			z := (v - med) / sigma
			if z > opts.ZThreshold && v > med*opts.MinRatio {
				alerts = append(alerts, Alert{s.Tenant, s.Endpoint, hour, "robust-z", v, med})
			}
		}
	}
	return alerts
}
