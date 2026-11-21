package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/core"
)

func TestBoundedParallelRegions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	started := make(chan string, 12)
	var mu sync.Mutex
	active, maximum := 0, 0
	perRegion := map[string]int{}
	api := fakeReporter(func(ctx context.Context, q query) (core.ComputeCapacityReport, error) {
		mu.Lock()
		active++
		perRegion[q.Region]++
		maximum = max(maximum, active)
		if perRegion[q.Region] > 1 {
			t.Errorf("concurrent requests within region %s", q.Region)
		}
		mu.Unlock()
		started <- q.Region
		select {
		case <-release:
		case <-ctx.Done():
		}
		mu.Lock()
		active--
		perRegion[q.Region]--
		mu.Unlock()
		return report(q), nil
	})
	var queries []query
	for i := 0; i < 4; i++ {
		for j := 0; j < 3; j++ {
			q := fixture()
			q.Name, q.Region = fmt.Sprintf("q-%d-%d", i, j), fmt.Sprintf("region-%d", i)
			queries = append(queries, q)
		}
	}
	done := make(chan snapshot, 1)
	go func() { done <- collectWithConcurrency(ctx, api, queries, 2) }()
	// Both regions must reach the reporter before either request is released.
	first := ""
	for i := 0; i < 2; i++ {
		select {
		case region := <-started:
			if i == 0 {
				first = region
			} else if region == first {
				t.Error("workers did not scan distinct regions")
			}
		case <-ctx.Done():
			t.Fatal("regional requests did not overlap")
		}
	}
	close(release)
	select {
	case s := <-done:
		if !s.Success || len(s.Results) != len(queries) || maximum != 2 {
			t.Fatalf("snapshot=%+v max concurrency=%d", s, maximum)
		}
		for i, r := range s.Results {
			if r.Query.Name != queries[i].Name {
				t.Fatal("result order depends on worker completion order")
			}
		}
	case <-ctx.Done():
		t.Fatal("scan did not finish")
	}
}

func TestParallelFailureAndCancellation(t *testing.T) {
	var queries []query
	for i := 0; i < 3; i++ {
		q := fixture()
		q.Name, q.Region = fmt.Sprintf("q%d", i), fmt.Sprintf("region-%d", i)
		queries = append(queries, q)
	}
	api := fakeReporter(func(_ context.Context, q query) (core.ComputeCapacityReport, error) {
		if q.Region == "region-1" {
			return core.ComputeCapacityReport{}, errors.New("EOF")
		}
		return report(q), nil
	})
	s := collectWithConcurrency(context.Background(), api, queries, 3)
	if s.Success || len(s.Results) != 0 || len(s.Errors) != 1 {
		t.Fatalf("partial results were published: %+v", s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api = fakeReporter(func(context.Context, query) (core.ComputeCapacityReport, error) {
		t.Error("started request after cancellation")
		return core.ComputeCapacityReport{}, nil
	})
	s = collectWithConcurrency(ctx, api, queries, 2)
	if s.Success || len(s.Results) != 0 {
		t.Fatalf("cancelled scan succeeded: %+v", s)
	}
}
