package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

func TestPartialScanExposesIndependentObservations(t *testing.T) {
	good := fixture()
	good.ShapeConfig = &core.CapacityReportInstanceShapeConfig{Ocpus: common.Float32(4), MemoryInGBs: common.Float32(32)}
	bad := good
	bad.Name, bad.Region = "failed", "uk-cardiff-1"
	before := time.Now()
	s := collectWithConcurrency(context.Background(), fakeReporter(func(_ context.Context, q query) (core.ComputeCapacityReport, error) {
		if q.Name == bad.Name {
			return core.ComputeCapacityReport{}, errors.New("transient EOF")
		}
		r := report(q)
		r.ShapeAvailabilities[0].AvailableCount = common.Int64(7)
		return r, nil
	}), []query{good, bad}, 2)
	if s.Success || len(s.Results) != 1 || len(s.Failures) != 1 || s.Results[0].CheckedAt.Before(before) || s.Failures[0].CheckedAt.Before(before) {
		t.Fatalf("invalid partial snapshot: %+v", s)
	}
	m := metrics(s)
	for _, expected := range []string{
		"oci_capacity_collection_success 0",
		"oci_capacity_available_count{" + metricLabels(good, "") + "} 7",
		"oci_capacity_state{" + metricLabels(good, "") + "} 1",
		"oci_capacity_state{" + metricLabels(bad, "") + "} 0",
		"oci_capacity_checked_timestamp_seconds{" + metricLabels(good, "") + fmt.Sprintf("} %v", timestamp(s.Results[0].CheckedAt)),
		`ocpus="4",memory_gbs="32"`,
	} {
		if !strings.Contains(m, expected) {
			t.Errorf("missing %q in metrics", expected)
		}
	}
	for _, name := range []string{"oci_capacity_available_count", "oci_capacity_available"} {
		if strings.Contains(m, name+"{"+metricLabels(bad, "")) {
			t.Errorf("failed check fabricated capacity in %s", name)
		}
	}
	st := &store{}
	st.publish(s)
	r := httptest.NewRecorder()
	st.handler().ServeHTTP(r, httptest.NewRequest("GET", "/results", nil))
	var decoded snapshot
	if err := json.Unmarshal(r.Body.Bytes(), &decoded); err != nil || len(decoded.Results) != 1 || decoded.Failures[0].Stage != "capacity" || decoded.Failures[0].Error != "transient EOF" {
		t.Fatalf("invalid /results response: %s", r.Body.String())
	}
}

func TestNewFailureReplacesOldAvailableObservation(t *testing.T) {
	q := fixture()
	st := &store{}
	st.publish(collect(context.Background(), fakeReporter(func(context.Context, query) (core.ComputeCapacityReport, error) { return report(q), nil }), []query{q}))
	lastSuccess := st.s.LastSuccess
	st.publish(collect(context.Background(), fakeReporter(func(context.Context, query) (core.ComputeCapacityReport, error) {
		return core.ComputeCapacityReport{}, errors.New("denied")
	}), []query{q}))
	m := metrics(st.s)
	if !strings.Contains(m, "oci_capacity_state{"+metricLabels(q, "")+"} 0") || strings.Contains(m, "oci_capacity_available{") || !st.s.LastSuccess.Equal(lastSuccess) {
		t.Fatalf("old capacity was reused after failure: %s", m)
	}
	st.publish(collect(context.Background(), fakeReporter(func(context.Context, query) (core.ComputeCapacityReport, error) { return report(q), nil }), []query{q}))
	if len(st.s.Failures) != 0 || !strings.Contains(metrics(st.s), "oci_capacity_state{"+metricLabels(q, "")+"} 1") {
		t.Fatal("recovery did not replace the failed observation")
	}
}

func TestDistinctCapacityStatesAndPerObservationTime(t *testing.T) {
	for status, state := range map[string]int{"AVAILABLE": 1, "OUT_OF_HOST_CAPACITY": 2, "HARDWARE_NOT_SUPPORTED": 3} {
		q := fixture()
		r := report(q)
		r.ShapeAvailabilities[0].AvailabilityStatus = core.CapacityReportShapeAvailabilityAvailabilityStatusEnum(status)
		checked := time.Now().Add(-time.Hour).Truncate(time.Second)
		s := snapshot{Attempt: time.Now(), Success: true, Results: []result{{Query: q, Report: r, CheckedAt: checked}}}
		m := metrics(s)
		if !strings.Contains(m, fmt.Sprintf("oci_capacity_state{%s} %d", metricLabels(q, ""), state)) || !strings.Contains(m, fmt.Sprintf("oci_capacity_checked_timestamp_seconds{%s} %v", metricLabels(q, ""), timestamp(checked))) {
			t.Fatal(m)
		}
		if strings.Contains(m, "oci_capacity_available_count{") {
			t.Fatal("unknown count was converted to zero")
		}
	}
}

func TestDiscoveryFailureHasUnknownScope(t *testing.T) {
	q := fixture()
	q.Region, q.AvailabilityDomain = "", ""
	s := collect(context.Background(), fakeReporter(nil), []query{q})
	if len(s.Failures) != 1 || s.Failures[0].Stage != "regions" || !strings.Contains(metrics(s), `region="UNKNOWN",availability_domain="UNKNOWN"`) {
		t.Fatalf("discovery failure was hidden: %+v", s)
	}
}

func TestMalformedResponseOnlyInvalidatesAffectedQuery(t *testing.T) {
	good := fixture()
	bad := good
	bad.Name = "malformed"
	s := collect(context.Background(), fakeReporter(func(_ context.Context, q query) (core.ComputeCapacityReport, error) {
		r := report(q)
		if q.Name == bad.Name {
			r.ShapeAvailabilities[0].AvailabilityStatus = "NEW_UNKNOWN_STATUS"
		}
		return r, nil
	}), []query{good, bad})
	if s.Success || len(s.Results) != 1 || len(s.Failures) != 1 || s.Failures[0].Query.Name != bad.Name || !strings.Contains(metrics(s), "oci_capacity_state{"+metricLabels(bad, "")+"} 0") {
		t.Fatalf("malformed response was not isolated: %+v", s)
	}
}
