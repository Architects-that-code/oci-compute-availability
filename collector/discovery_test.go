package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/core"
)

type discoveryReporter struct {
	fakeReporter
	discover func(context.Context, string) ([]string, error)
}

func (f discoveryReporter) AvailabilityDomains(ctx context.Context, region string) ([]string, error) {
	return f.discover(ctx, region)
}

func TestDiscoverAllDomainsPerRegion(t *testing.T) {
	calls := map[string]int{}
	var reported []query
	api := discoveryReporter{
		fakeReporter: func(_ context.Context, q query) (core.ComputeCapacityReport, error) {
			reported = append(reported, q)
			return report(q), nil
		},
		discover: func(_ context.Context, region string) ([]string, error) {
			calls[region]++
			if region == "us-ashburn-1" {
				return []string{"iad:AD-1", "iad:AD-2", "iad:AD-3"}, nil
			}
			return []string{"lhr:AD-1"}, nil
		},
	}
	q := fixture()
	q.AvailabilityDomain = ""
	q2 := q
	q2.Name = "another-shape"
	q2.Shape = "VM.Standard2.1"
	q3 := q
	q3.Name, q3.Region = "london", "uk-london-1"
	s := collect(context.Background(), api, []query{q, q2, q3})
	if !s.Success || len(s.Results) != 7 || len(reported) != 7 || calls["us-ashburn-1"] != 1 || calls["uk-london-1"] != 1 {
		t.Fatalf("snapshot=%+v calls=%v reported=%d", s, calls, len(reported))
	}
	for i, r := range s.Results {
		if r.Query.AvailabilityDomain == "" || r.Query.AvailabilityDomain != *r.Report.AvailabilityDomain {
			t.Fatalf("unresolved result %d: %+v", i, r)
		}
	}
	if !strings.Contains(metrics(s), `availability_domain="iad:AD-3"`) {
		t.Fatal("missing discovered AD in metrics")
	}
	// Discovery is refreshed on the next scan, not cached indefinitely.
	collect(context.Background(), api, []query{q})
	if calls["us-ashburn-1"] != 2 {
		t.Fatal(calls)
	}
}

func TestDiscoveryFailurePreservesExplicitResults(t *testing.T) {
	for _, discoveryErr := range []error{errors.New("denied"), nil} {
		calls := 0
		api := discoveryReporter{
			fakeReporter: func(_ context.Context, q query) (core.ComputeCapacityReport, error) { return report(q), nil },
			discover: func(context.Context, string) ([]string, error) {
				calls++
				return nil, discoveryErr
			},
		}
		explicit := fixture()
		implicit := explicit
		implicit.Name, implicit.AvailabilityDomain = "implicit", ""
		other := implicit
		other.Name = "other"
		s := collect(context.Background(), api, []query{explicit, implicit, other})
		if s.Success || len(s.Results) != 1 || len(s.Errors) != 2 || calls != 1 {
			t.Fatalf("snapshot=%+v discovery calls=%d", s, calls)
		}
	}
}

func TestFailureInDiscoveredDomainPreservesResults(t *testing.T) {
	api := discoveryReporter{
		fakeReporter: func(_ context.Context, q query) (core.ComputeCapacityReport, error) {
			if q.AvailabilityDomain == "AD-2" {
				return core.ComputeCapacityReport{}, errors.New("report failed")
			}
			return report(q), nil
		},
		discover: func(context.Context, string) ([]string, error) { return []string{"AD-1", "AD-2"}, nil },
	}
	q := fixture()
	q.AvailabilityDomain = ""
	s := collect(context.Background(), api, []query{q})
	if s.Success || len(s.Results) != 1 || len(s.Errors) != 1 || !strings.Contains(s.Errors[0], "AD-2") {
		t.Fatalf("snapshot=%+v", s)
	}
}
