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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type result struct {
	Query     query                      `json:"query"`
	Report    core.ComputeCapacityReport `json:"report"`
	CheckedAt time.Time                  `json:"checkedAt"`
}
type failure struct {
	Query     query     `json:"query"`
	CheckedAt time.Time `json:"checkedAt"`
	Stage     string    `json:"stage"`
	Error     string    `json:"error"`
}

type snapshot struct {
	Attempt     time.Time `json:"attempt"`
	LastSuccess time.Time `json:"lastSuccess"`
	Duration    float64   `json:"durationSeconds"`
	Success     bool      `json:"success"`
	Errors      []string  `json:"errors,omitempty"`
	Results     []result  `json:"results"`
	Failures    []failure `json:"failures,omitempty"`
}

// fail records an unknown outcome, never a negative capacity observation.
func (s *snapshot) fail(q query, stage string, err error) {
	s.Failures = append(s.Failures, failure{Query: q, CheckedAt: time.Now(), Stage: stage, Error: err.Error()})
	s.Errors = append(s.Errors, fmt.Sprintf("%s (%s, %s): %s: %v", q.Name, q.Region, q.AvailabilityDomain, stage, err))
}

// A projected ConfigMap volume contains one JSON file per region. Read these
// into one process without exceeding the Kubernetes per-ConfigMap size limit.
func loadQueries(path string) ([]query, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return loadQueryFile(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var queries []query
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fileQueries, err := loadQueryFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		for _, q := range fileQueries {
			if seen[q.Name] {
				return nil, fmt.Errorf("duplicate query name across files: %s", q.Name)
			}
			seen[q.Name] = true
			queries = append(queries, q)
		}
		if len(queries) > 100000 {
			return nil, errors.New("configure at most 100000 queries across the directory")
		}
	}
	if len(queries) == 0 {
		return nil, errors.New("query directory must contain nonempty .json query files")
	}
	return queries, nil
}

func loadQueryFile(path string) ([]query, error) {
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
		if q.Name == "" || q.Shape == "" || seen[q.Name] {
			return nil, errors.New("each query requires unique name and shape")
		}
		if q.AvailabilityDomain != "" && q.Region == "" {
			return nil, errors.New("an explicit availabilityDomain requires a region")
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
	return collectWithConcurrency(ctx, api, qs, 1)
}

func collectWithConcurrency(ctx context.Context, api reporter, qs []query, concurrency int) snapshot {
	s := snapshot{Attempt: time.Now()}
	var expanded []query
	var regions []string
	var regionErr error
	regionsLoaded := false
	for _, q := range qs {
		if q.Region != "" {
			expanded = append(expanded, q)
			continue
		}
		if !regionsLoaded {
			regions, regionErr = api.SubscribedRegions(ctx)
			if regionErr == nil && len(regions) == 0 {
				regionErr = errors.New("no subscribed regions returned")
			}
			regionsLoaded = true
		}
		if regionErr != nil {
			s.fail(q, "regions", regionErr)
			continue
		}
		for _, region := range regions {
			resolved := q
			resolved.Region = region
			expanded = append(expanded, resolved)
		}
	}
	// One worker owns each region, keeping requests within a region sequential.
	var regionOrder []string
	byRegion := map[string][]query{}
	for _, q := range expanded {
		if _, ok := byRegion[q.Region]; !ok {
			regionOrder = append(regionOrder, q.Region)
		}
		byRegion[q.Region] = append(byRegion[q.Region], q)
	}
	if concurrency < 1 {
		concurrency = 1
	}
	workers := min(concurrency, len(regionOrder))
	log.Printf("capacity scan: regions=%d configured_queries=%d regional_queries=%d concurrency=%d", len(regionOrder), len(qs), len(expanded), workers)
	outcomes := make([]snapshot, len(regionOrder))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				region := regionOrder[i]
				started := time.Now()
				log.Printf("capacity region: region=%s started queries=%d", region, len(byRegion[region]))
				outcomes[i] = collectRegion(ctx, api, byRegion[region])
				log.Printf("capacity region: region=%s completed results=%d errors=%d duration=%.1fs", region, len(outcomes[i].Results), len(outcomes[i].Errors), time.Since(started).Seconds())
			}
		}()
	}
	for i := range regionOrder {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	// Merge in configuration order, independent of worker completion order.
	for _, outcome := range outcomes {
		s.Results = append(s.Results, outcome.Results...)
		s.Errors = append(s.Errors, outcome.Errors...)
		s.Failures = append(s.Failures, outcome.Failures...)
	}
	s.Duration = time.Since(s.Attempt).Seconds()
	s.Success = len(s.Errors) == 0
	if s.Success {
		s.LastSuccess = time.Now()
	}
	return s
}

// collectRegion is called once per region in a scan.
func collectRegion(ctx context.Context, api reporter, qs []query) snapshot {
	var s snapshot
	type discovery struct {
		names []string
		err   error
	}
	domains := map[string]discovery{}
	for _, q := range qs {
		names := []string{q.AvailabilityDomain}
		if q.AvailabilityDomain == "" {
			d, ok := domains[q.Region]
			if !ok {
				if err := ctx.Err(); err != nil {
					d.err = err
				} else {
					d.names, d.err = api.AvailabilityDomains(ctx, q.Region)
				}
				if d.err == nil && len(d.names) == 0 {
					d.err = errors.New("no availability domains returned")
				}
				domains[q.Region] = d
			}
			if d.err != nil {
				s.fail(q, "availability_domains", d.err)
				continue
			}
			names = d.names
		}
		for _, name := range names {
			resolved := q
			resolved.AvailabilityDomain = name
			if err := ctx.Err(); err != nil {
				s.fail(resolved, "cancelled", err)
				continue
			}
			r, err := api.Report(ctx, resolved)
			if err == nil {
				err = validateReport(resolved, r)
			}
			if err != nil {
				s.fail(resolved, "capacity", err)
				continue
			}
			s.Results = append(s.Results, result{Query: resolved, Report: r, CheckedAt: time.Now()})
		}
	}
	return s
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
	queries := flag.String("queries", "", "Required non-secret JSON query file or directory")
	interval := flag.Duration("interval", 5*time.Minute, "Pause after each completed scan (minimum 30s)")
	timeout := flag.Duration("timeout", 10*time.Minute, "Maximum duration of an entire scan")
	concurrency := flag.Int("concurrency", 6, "Maximum regions scanned concurrently (1..32); requests within each region are sequential")
	listen := flag.String("listen", ":9109", "HTTP listen address")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, "OCI Compute availability collector\n\nUsage: %s [options]\n\nCollect capacity observations for configured regions, availability domains and shapes.\nWith no arguments, print this help and exit.\n\nOptions:\n", os.Args[0])
		flag.PrintDefaults()
		fmt.Fprint(out, `
Environment:
  OCI_TENANCY_OCID  Required for workload, instance and resource authentication.
  OCI_RESOURCE_PRINCIPAL_VERSION=2.2 and OCI_RESOURCE_PRINCIPAL_REGION
                   Required for OKE workload identity.

Examples:
  collector --auth workload --queries queries.json --interval 5m
  collector --auth instance --queries queries.json
  collector --auth resource --queries queries.json
  collector --once --auth config --config-file /secure/path/oci.properties --queries queries.json

Queries:
  Supply a JSON array with unique name and shape.
  Omit region to scan all subscribed regions; set region to restrict a query.
  Omit availabilityDomain to discover and query all ADs in each region.
  Set availabilityDomain with region to restrict a query to one AD.
  Flex shapes require shapeConfig.ocpus and shapeConfig.memoryInGBs.
  Optional faultDomain narrows the request. See queries.example.json.
  A directory merges .json files into one global scan (1..1000 queries/file).

Service endpoints:
  /metrics  Prometheus metrics
  /results  Latest complete JSON snapshot
  /healthz  Process liveness

Capacity is an observation, not a reservation or launch guarantee.
`)
	}
	if len(os.Args) == 1 {
		flag.CommandLine.SetOutput(os.Stdout)
		flag.Usage()
		return nil
	}
	flag.Parse()
	o := authOptions{mode: *mode, configFile: *config, profile: *profile, once: *once}
	if err := o.validate(); err != nil {
		return err
	}
	if *interval < 30*time.Second || *timeout <= 0 {
		return errors.New("interval must be >=30s and timeout positive")
	}
	if *concurrency < 1 || *concurrency > 32 {
		return errors.New("concurrency must be between 1 and 32")
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
				s := snapshot{Attempt: time.Now()}
				for _, q := range qs {
					s.fail(q, "authentication", e)
				}
				return s
			}
			api = &ociReporter{provider: p, tenancy: tenancy, clients: map[string]core.ComputeClient{}}
		}
		return collectWithConcurrency(c, api, qs, *concurrency)
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
