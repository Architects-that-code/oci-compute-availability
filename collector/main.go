// Periodic OCI capacity observations, never a capacity reservation or launch guarantee.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/oracle/oci-go-sdk/v65/core"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type result struct {
	Query  query                      `json:"query"`
	Report core.ComputeCapacityReport `json:"report"`
}
type snapshot struct {
	Attempt     time.Time `json:"attempt"`
	LastSuccess time.Time `json:"lastSuccess"`
	Duration    float64   `json:"durationSeconds"`
	Success     bool      `json:"success"`
	Errors      []string  `json:"errors,omitempty"`
	Results     []result  `json:"results"`
}

func loadQueries(path string) ([]query, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1024*1024))
	d.DisallowUnknownFields()
	var qs []query
	if err = d.Decode(&qs); err != nil {
		return nil, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return nil, errors.New("query file must contain one JSON array")
	}
	if len(qs) == 0 || len(qs) > 1000 {
		return nil, errors.New("configure 1..1000 queries")
	}
	seen := map[string]bool{}
	for _, q := range qs {
		if q.Name == "" || q.Region == "" || q.AvailabilityDomain == "" || q.Shape == "" || seen[q.Name] {
			return nil, errors.New("each query requires unique name, region, availabilityDomain, and shape")
		}
		seen[q.Name] = true
		if strings.HasSuffix(q.Shape, ".Flex") && (q.ShapeConfig == nil || q.ShapeConfig.Ocpus == nil || q.ShapeConfig.MemoryInGBs == nil) {
			return nil, fmt.Errorf("query %s: Flex shapes require explicit ocpus and memoryInGBs", q.Name)
		}
		if c := q.ShapeConfig; c != nil {
			for _, v := range []*float32{c.Ocpus, c.MemoryInGBs} {
				if v != nil && (*v <= 0 || math.IsNaN(float64(*v)) || math.IsInf(float64(*v), 0)) {
					return nil, fmt.Errorf("query %s: shape sizes must be positive finite numbers", q.Name)
				}
			}
			if c.Nvmes != nil && *c.Nvmes < 0 {
				return nil, errors.New("nvmes cannot be negative")
			}
			if _, err := c.ValidateEnumValue(); err != nil {
				return nil, err
			}
		}
	}
	return qs, nil
}

func validateReport(q query, r core.ComputeCapacityReport) error {
	if r.AvailabilityDomain == nil || *r.AvailabilityDomain != q.AvailabilityDomain || len(r.ShapeAvailabilities) == 0 {
		return errors.New("empty or mismatched availability-domain report")
	}
	seen := map[string]bool{}
	for _, s := range r.ShapeAvailabilities {
		if s.InstanceShape == nil || *s.InstanceShape != q.Shape {
			return errors.New("missing or mismatched shape")
		}
		fd := ""
		if s.FaultDomain != nil {
			fd = *s.FaultDomain
		}
		if q.FaultDomain != "" && fd != q.FaultDomain {
			return errors.New("mismatched fault domain")
		}
		if seen[fd] {
			return errors.New("duplicate shape/fault-domain result")
		}
		seen[fd] = true
		switch string(s.AvailabilityStatus) {
		case "AVAILABLE", "OUT_OF_HOST_CAPACITY", "HARDWARE_NOT_SUPPORTED":
		default:
			return errors.New("missing or unknown availability status")
		}
		if s.AvailableCount != nil && *s.AvailableCount < 0 {
			return errors.New("negative available count")
		}
	}
	return nil
}

func collect(ctx context.Context, api reporter, qs []query) snapshot {
	s := snapshot{Attempt: time.Now()}
	for _, q := range qs {
		r, err := api.Report(ctx, q)
		if err == nil {
			err = validateReport(q, r)
		}
		if err != nil {
			s.Errors = append(s.Errors, fmt.Sprintf("%s: %v", q.Name, err))
			continue
		}
		s.Results = append(s.Results, result{q, r})
	}
	s.Duration = time.Since(s.Attempt).Seconds()
	s.Success = len(s.Errors) == 0
	if s.Success {
		s.LastSuccess = time.Now()
	} else {
		s.Results = nil
	}
	return s
}

func metrics(s snapshot) string {
	var b strings.Builder
	gauge := func(n, help string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", n, help, n) }
	scalar := func(n, help string, v any) { gauge(n, help); fmt.Fprintf(&b, "%s %v\n", n, v) }
	success := 0
	if s.Success {
		success = 1
	}
	ts := func(t time.Time) float64 {
		if t.IsZero() {
			return 0
		}
		return float64(t.UnixNano()) / 1e9
	}
	scalar("oci_capacity_collection_success", "One when all configured queries succeeded.", success)
	scalar("oci_capacity_last_attempt_timestamp_seconds", "Latest attempt start time.", ts(s.Attempt))
	scalar("oci_capacity_last_success_timestamp_seconds", "Latest complete successful scan.", ts(s.LastSuccess))
	scalar("oci_capacity_collection_duration_seconds", "Latest scan duration.", s.Duration)
	scalar("oci_capacity_collection_errors", "Failed queries in latest scan.", len(s.Errors))
	var counts, available, statuses strings.Builder
	if s.Success {
		for _, r := range s.Results {
			for _, a := range r.Report.ShapeAvailabilities {
				fd := ""
				if a.FaultDomain != nil {
					fd = *a.FaultDomain
				}
				// The stable query name identifies the complete requested shape configuration.
				labels := fmt.Sprintf("query=%s,region=%s,availability_domain=%s,shape=%s,fault_domain=%s", strconv.Quote(r.Query.Name), strconv.Quote(r.Query.Region), strconv.Quote(r.Query.AvailabilityDomain), strconv.Quote(r.Query.Shape), strconv.Quote(fd))
				if a.AvailableCount != nil {
					fmt.Fprintf(&counts, "oci_capacity_available_count{%s} %d\n", labels, *a.AvailableCount)
				}
				v := 0
				if string(a.AvailabilityStatus) == "AVAILABLE" {
					v = 1
				}
				fmt.Fprintf(&available, "oci_capacity_available{%s} %d\n", labels, v)
				fmt.Fprintf(&statuses, "oci_capacity_status{%s,status=%s} 1\n", labels, strconv.Quote(string(a.AvailabilityStatus)))
			}
		}
	}
	gauge("oci_capacity_available_count", "Reported launchable instance count for the requested shape configuration; absent when unknown.")
	b.WriteString(counts.String())
	gauge("oci_capacity_available", "One if OCI reports AVAILABLE for this query.")
	b.WriteString(available.String())
	gauge("oci_capacity_status", "Reported capacity status, with value one.")
	b.WriteString(statuses.String())
	return b.String()
}

type store struct {
	sync.RWMutex
	s snapshot
}

func (st *store) publish(s snapshot) {
	st.Lock()
	defer st.Unlock()
	if !s.Success {
		s.LastSuccess = st.s.LastSuccess
	}
	st.s = s
}
func (st *store) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		st.RLock()
		defer st.RUnlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, metrics(st.s))
	})
	mux.HandleFunc("/results", func(w http.ResponseWriter, r *http.Request) {
		st.RLock()
		defer st.RUnlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st.s)
	})
	return mux
}
func run() error {
	mode := flag.String("auth", "workload", "workload (OKE), instance (OCI Compute), resource (Container Instances), config (local --once only)")
	config := flag.String("config-file", "", "Explicit local OCI properties file")
	profile := flag.String("profile", "DEFAULT", "Local OCI profile")
	once := flag.Bool("once", false, "Collect once, print JSON and exit")
	queries := flag.String("queries", "", "Required non-secret JSON query file")
	interval := flag.Duration("interval", 5*time.Minute, "Pause after each completed scan (minimum 30s)")
	timeout := flag.Duration("timeout", 10*time.Minute, "Maximum duration of an entire scan")
	listen := flag.String("listen", ":9109", "HTTP listen address")
	flag.Parse()
	o := authOptions{mode: *mode, configFile: *config, profile: *profile, once: *once}
	if err := o.validate(); err != nil {
		return err
	}
	if *interval < 30*time.Second || *timeout <= 0 {
		return errors.New("interval must be >=30s and timeout positive")
	}
	qs, err := loadQueries(*queries)
	if err != nil {
		return err
	}
	tenancy := os.Getenv("OCI_TENANCY_OCID")
	if *mode != "config" && tenancy == "" {
		return errors.New("OCI_TENANCY_OCID is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var api reporter
	scan := func() snapshot {
		c, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		if api == nil {
			p, e := o.provider()
			if e == nil && *mode == "config" {
				tenancy, e = p.TenancyOCID()
			}
			if e != nil {
				return snapshot{Attempt: time.Now(), Errors: []string{e.Error()}}
			}
			api = &ociReporter{provider: p, tenancy: tenancy, clients: map[string]core.ComputeClient{}}
		}
		return collect(c, api, qs)
	}
	if *once {
		s := scan()
		if err := json.NewEncoder(os.Stdout).Encode(s); err != nil {
			return err
		}
		if !s.Success {
			return errors.New("capacity scan incomplete")
		}
		return nil
	}
	st := &store{}
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			s := scan()
			st.publish(s)
			log.Printf("capacity scan: success=%t errors=%d duration=%.1fs", s.Success, len(s.Errors), s.Duration)
			for _, e := range s.Errors {
				log.Print(e)
			}
			timer := time.NewTimer(*interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	server := &http.Server{Addr: *listen, Handler: st.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
