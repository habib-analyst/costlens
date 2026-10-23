// costlens — a cost-per-request FinOps engine whose attribution is
// validated against ground-truth metering.
//
// Subcommands:
//
//	gen      -seed N [-anomalies] -out events.jsonl   generate a workload (+ truth sidecar)
//	validate -seed N [-out results.json]              full measured validation run
//	serve    -addr :8080                              HTTP ingest + rollup query API
//	rollup   [-tenant T] [-endpoint E] [-model M] events.jsonl
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"costlens/internal/eval"
	"costlens/internal/event"
	"costlens/internal/pricing"
	"costlens/internal/rollup"
	"costlens/internal/truth"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		err = cmdGen(os.Args[2:])
	case "validate":
		err = cmdValidate(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "rollup":
		err = cmdRollup(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "costlens:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: costlens <gen|validate|serve|rollup> [flags]")
}

func engine() (*pricing.Engine, error) {
	t, err := pricing.DefaultTable()
	if err != nil {
		return nil, err
	}
	return pricing.New(t), nil
}

// ---- gen ----

func cmdGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	seed := fs.Int64("seed", 42, "workload seed")
	anom := fs.Bool("anomalies", false, "inject labelled anomalies")
	out := fs.String("out", "events.jsonl", "output JSONL path")
	fs.Parse(args)

	w := truth.Generate(*seed, *anom)
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, e := range w.Events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	// Truth sidecar: the harness's own accounting, for external auditors.
	sidecar := struct {
		TrueMicro map[string]int64    `json:"true_micro_usd"`
		Anomalies []truth.AnomalyCell `json:"anomalies"`
	}{w.TrueMicro, w.Anomalies}
	tf, err := os.Create(strings.TrimSuffix(*out, ".jsonl") + ".truth.json")
	if err != nil {
		return err
	}
	defer tf.Close()
	tenc := json.NewEncoder(tf)
	tenc.SetIndent("", "  ")
	if err := tenc.Encode(sidecar); err != nil {
		return err
	}
	fmt.Printf("wrote %d events to %s (anomalies: %d cells)\n", len(w.Events), *out, len(w.Anomalies))
	return nil
}

// ---- validate ----

type validationOutput struct {
	Seed        int64               `json:"seed"`
	Attribution *eval.Report        `json:"attribution"`
	Anomalies   *eval.AnomalyReport `json:"anomaly_detection"`
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	seed := fs.Int64("seed", 42, "workload seed")
	out := fs.String("out", "results.json", "results JSON path")
	fs.Parse(args)

	eng, err := engine()
	if err != nil {
		return err
	}
	base := truth.Generate(*seed, false)
	rep, err := eval.Evaluate(base, eng)
	if err != nil {
		return err
	}
	anomW := truth.Generate(*seed, true)
	anomRep, err := eval.EvaluateAnomalies(anomW, eng)
	if err != nil {
		return err
	}
	res := validationOutput{Seed: *seed, Attribution: rep, Anomalies: anomRep}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		return err
	}

	fmt.Printf("events: %d   true total: $%.2f\n", rep.Events, rep.TrueTotalUSD)
	fmt.Printf("costlens attribution error: %.4f%% total | worst slice %q: %.4f%%\n",
		rep.EngineTotalErrPct, rep.WorstSlice.Slice, rep.WorstSlice.AbsErrPct)
	fmt.Printf("naive attributor error:     %.4f%% total\n", rep.NaiveTotalErrPct)
	for _, c := range rep.Cases {
		fmt.Printf("  case %-15s n=%-6d engine %.4f%%   naive %.2f%%\n",
			c.Case, c.Events, c.EngineAbsErrPct, c.NaiveAbsErrPct)
	}
	fmt.Printf("anomaly detection: precision %.3f recall %.3f (%d/%d cells, %d alerts) ttd %v\n",
		anomRep.Precision, anomRep.Recall, anomRep.TruePositives, anomRep.TrueCells, anomRep.AlertCells, anomRep.TimeToDetectHrs)
	fmt.Printf("wrote %s\n", *out)
	return nil
}

// ---- serve ----

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "listen address")
	fs.Parse(args)

	eng, err := engine()
	if err != nil {
		return err
	}
	var mu sync.Mutex
	store := rollup.NewStore()

	mux := http.NewServeMux()
	mux.HandleFunc("/ingest", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(rw, "POST only", http.StatusMethodNotAllowed)
			return
		}
		events, err := event.ReadJSONL(r.Body)
		if err != nil {
			http.Error(rw, "bad JSONL: "+err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, e := range events {
			c, err := eng.Cost(e)
			if err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			store.Add(e, c)
			n++
		}
		fmt.Fprintf(rw, `{"ingested":%d}`+"\n", n)
	})
	mux.HandleFunc("/rollup", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		hour := int64(-1)
		if h := q.Get("hour"); h != "" {
			fmt.Sscanf(h, "%d", &hour)
		}
		f := rollup.Key{Tenant: q.Get("tenant"), Endpoint: q.Get("endpoint"), Model: q.Get("model"), Hour: hour}
		mu.Lock()
		cells := store.Query(f)
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		json.NewEncoder(rw).Encode(cells)
	})
	fmt.Println("costlens listening on", *addr)
	return http.ListenAndServe(*addr, mux)
}

// ---- rollup CLI ----

func cmdRollup(args []string) error {
	fs := flag.NewFlagSet("rollup", flag.ExitOnError)
	tenant := fs.String("tenant", "", "filter: tenant")
	endpoint := fs.String("endpoint", "", "filter: endpoint")
	model := fs.String("model", "", "filter: served model")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: costlens rollup [flags] events.jsonl")
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	events, err := event.ReadJSONL(f)
	if err != nil {
		return err
	}
	eng, err := engine()
	if err != nil {
		return err
	}
	store := rollup.NewStore()
	for _, e := range events {
		c, err := eng.Cost(e)
		if err != nil {
			return err
		}
		store.Add(e, c)
	}
	cells := store.Query(rollup.Key{Tenant: *tenant, Endpoint: *endpoint, Model: *model, Hour: -1})
	sort.Slice(cells, func(i, j int) bool { return cells[i].Cost > cells[j].Cost })
	for _, c := range cells {
		if c.Key.Hour != -1 {
			continue
		}
		fmt.Printf("%-10s %-16s %-14s $%10.2f  (%d requests)\n",
			c.Key.Tenant, c.Key.Endpoint, c.Key.Model, c.Cost, c.Count)
	}
	fmt.Printf("TOTAL $%.2f over %d events\n", store.Total(), len(events))
	return nil
}
