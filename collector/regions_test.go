package main

import (
	"context"
	"errors"
	"testing"

	"github.com/oracle/oci-go-sdk/v65/core"
)

type regionReporter struct {
	discoveryReporter
	regions func(context.Context) ([]string, error)
}

func (f regionReporter) SubscribedRegions(ctx context.Context) ([]string, error) {
	return f.regions(ctx)
}

func TestAllSubscribedRegionsAndExplicitFilter(t *testing.T) {
	regionCalls := 0
	domainCalls := map[string]int{}
	api := regionReporter{
		regions: func(context.Context) ([]string, error) {
			regionCalls++
			return []string{"us-ashburn-1", "uk-london-1"}, nil
		},
		discoveryReporter: discoveryReporter{
			fakeReporter: func(_ context.Context, q query) (core.ComputeCapacityReport, error) { return report(q), nil },
			discover: func(_ context.Context, region string) ([]string, error) {
				domainCalls[region]++
				return []string{region + ":AD-1", region + ":AD-2"}, nil
			},
		},
	}
	global := fixture()
	global.Region, global.AvailabilityDomain = "", ""
	other := global
	other.Name = "another-size"
	explicit := global
	explicit.Name, explicit.Region = "filtered", "us-ashburn-1"
	s := collect(context.Background(), api, []query{global, other, explicit})
	if !s.Success || len(s.Results) != 10 || regionCalls != 1 || domainCalls["us-ashburn-1"] != 1 || domainCalls["uk-london-1"] != 1 {
		t.Fatalf("snapshot=%+v regionCalls=%d domainCalls=%v", s, regionCalls, domainCalls)
	}
	counts := map[string]int{}
	for _, result := range s.Results {
		counts[result.Query.Region]++
		if result.Query.Region == "" || result.Query.AvailabilityDomain == "" || (result.Query.Name == "filtered" && result.Query.Region != explicit.Region) {
			t.Fatal(result)
		}
	}
	if counts["us-ashburn-1"] != 6 || counts["uk-london-1"] != 4 {
		t.Fatal(counts)
	}
	collect(context.Background(), api, []query{global})
	if regionCalls != 2 {
		t.Fatal("subscriptions were not refreshed on the next scan")
	}
}

func TestRegionDiscoveryFailureSuppressesPartialResults(t *testing.T) {
	for _, err := range []error{nil, errors.New("denied")} {
		calls := 0
		api := regionReporter{
			regions: func(context.Context) ([]string, error) { calls++; return nil, err },
			discoveryReporter: discoveryReporter{
				fakeReporter: func(_ context.Context, q query) (core.ComputeCapacityReport, error) { return report(q), nil },
			},
		}
		q := fixture()
		global := q
		global.Name, global.Region, global.AvailabilityDomain = "global", "", ""
		s := collect(context.Background(), api, []query{q, global, global})
		if s.Success || len(s.Results) != 0 || len(s.Errors) != 2 || calls != 1 {
			t.Fatalf("snapshot=%+v calls=%d", s, calls)
		}
	}
}
