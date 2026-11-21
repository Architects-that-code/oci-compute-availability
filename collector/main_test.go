package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeReporter func(context.Context, query) (core.ComputeCapacityReport, error)

func (f fakeReporter) SubscribedRegions(context.Context) ([]string, error) {
	return nil, errors.New("unexpected subscribed-region discovery")
}

func (f fakeReporter) AvailabilityDomains(context.Context, string) ([]string, error) {
	return nil, errors.New("unexpected availability-domain discovery")
}

func (f fakeReporter) Report(c context.Context, q query) (core.ComputeCapacityReport, error) {
	return f(c, q)
}
func fixture() query {
	return query{Name: "e5-small", Region: "us-ashburn-1", AvailabilityDomain: "test:AD-1", Shape: "VM.Standard.E5.Flex"}
}
func report(q query) core.ComputeCapacityReport {
	return core.ComputeCapacityReport{AvailabilityDomain: &q.AvailabilityDomain, ShapeAvailabilities: []core.CapacityReportShapeAvailability{{InstanceShape: &q.Shape, AvailabilityStatus: core.CapacityReportShapeAvailabilityAvailabilityStatusAvailable}}}
}
func TestUnknownCountsAndZeroCounts(t *testing.T) {
	q := fixture()
	r := report(q)
	api := fakeReporter(func(context.Context, query) (core.ComputeCapacityReport, error) { return r, nil })
	s := collect(context.Background(), api, []query{q})
	if !s.Success || strings.Contains(metrics(s), "oci_capacity_available_count{") {
		t.Fatal("unknown count must be omitted")
	}
	count := int64(0)
	r.ShapeAvailabilities[0].AvailableCount = &count
	s = collect(context.Background(), api, []query{q})
	if !strings.Contains(metrics(s), "fault_domain=\"\"} 0") {
		t.Fatal(metrics(s))
	}
}
func TestPartialFailureSuppressesResults(t *testing.T) {
	q := fixture()
	q2 := q
	q2.Name = "failing"
	api := fakeReporter(func(_ context.Context, q query) (core.ComputeCapacityReport, error) {
		if q.Name == "failing" {
			return core.ComputeCapacityReport{}, errors.New("denied")
		}
		return report(q), nil
	})
	s := collect(context.Background(), api, []query{q, q2})
	if s.Success || len(s.Errors) != 1 || len(s.Results) != 0 || strings.Contains(metrics(s), "oci_capacity_available{") {
		t.Fatal(s)
	}
	st := &store{s: snapshot{LastSuccess: time.Now()}}
	last := st.s.LastSuccess
	st.publish(s)
	if !st.s.LastSuccess.Equal(last) {
		t.Fatal("lost last-success timestamp")
	}
}
func TestMalformedReports(t *testing.T) {
	q := fixture()
	for _, edit := range []func(*core.ComputeCapacityReport){
		func(r *core.ComputeCapacityReport) { r.ShapeAvailabilities = nil },
		func(r *core.ComputeCapacityReport) { r.AvailabilityDomain = common.String("wrong") },
		func(r *core.ComputeCapacityReport) { r.ShapeAvailabilities[0].AvailabilityStatus = "NEW_UNKNOWN_VALUE" },
		func(r *core.ComputeCapacityReport) {
			r.ShapeAvailabilities = append(r.ShapeAvailabilities, r.ShapeAvailabilities[0])
		},
	} {
		r := report(q)
		edit(&r)
		if validateReport(q, r) == nil {
			t.Fatal("accepted malformed report")
		}
	}
}
func TestQueriesValidation(t *testing.T) {
	for _, input := range []string{`[]`, `[{"name":"q","region":"iad","availabilityDomain":"ad","shape":"VM.Standard.E5.Flex"}]`, `[{"name":"q","region":"iad","availabilityDomain":"ad","shape":"fixed","typo":1}]`, `[] {}`} {
		path := filepath.Join(t.TempDir(), "queries.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadQueries(path); err == nil {
			t.Fatal("accepted invalid queries", input)
		}
	}
	if _, err := loadQueries("../queries.example.json"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPExposesSnapshotAndDiagnostics(t *testing.T) {
	q := fixture()
	s := collect(context.Background(), fakeReporter(func(context.Context, query) (core.ComputeCapacityReport, error) { return report(q), nil }), []query{q})
	st := &store{}
	st.publish(s)
	h := st.handler()
	for _, path := range []string{"/healthz", "/metrics", "/results"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", path, nil))
		if r.Code != 200 {
			t.Fatalf("%s status %d", path, r.Code)
		}
		if path == "/results" {
			var got snapshot
			if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil || !got.Success || len(got.Results) != 1 {
				t.Fatalf("invalid JSON snapshot %s", r.Body.String())
			}
		}
	}
	st.publish(snapshot{Errors: []string{"OCI denied"}})
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(r.Body.String(), "oci_capacity_collection_success 0") || strings.Contains(r.Body.String(), "oci_capacity_available{") {
		t.Fatal(r.Body.String())
	}
}
