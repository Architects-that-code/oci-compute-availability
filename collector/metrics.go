package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func timestamp(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixNano()) / 1e9
}

func metricLabels(q query, fd string) string {
	region, ad := q.Region, q.AvailabilityDomain
	if region == "" {
		region = "UNKNOWN"
	}
	if ad == "" {
		ad = "UNKNOWN"
	}
	ocpus, memory := "", ""
	if c := q.ShapeConfig; c != nil {
		if c.Ocpus != nil {
			ocpus = strconv.FormatFloat(float64(*c.Ocpus), 'f', -1, 32)
		}
		if c.MemoryInGBs != nil {
			memory = strconv.FormatFloat(float64(*c.MemoryInGBs), 'f', -1, 32)
		}
	}
	return fmt.Sprintf("query=%s,region=%s,availability_domain=%s,shape=%s,ocpus=%s,memory_gbs=%s,fault_domain=%s",
		strconv.Quote(q.Name), strconv.Quote(region), strconv.Quote(ad), strconv.Quote(q.Shape), strconv.Quote(ocpus), strconv.Quote(memory), strconv.Quote(fd))
}

func metrics(s snapshot) string {
	var b strings.Builder
	gauge := func(n, help string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", n, help, n) }
	scalar := func(n, help string, v any) { gauge(n, help); fmt.Fprintf(&b, "%s %v\n", n, v) }
	success := 0
	if s.Success {
		success = 1
	}
	scalar("oci_capacity_collection_success", "One when all configured queries succeeded; zero may include useful partial results.", success)
	scalar("oci_capacity_last_attempt_timestamp_seconds", "Latest attempt start time.", timestamp(s.Attempt))
	scalar("oci_capacity_last_success_timestamp_seconds", "Latest complete successful scan.", timestamp(s.LastSuccess))
	scalar("oci_capacity_collection_duration_seconds", "Latest scan duration.", s.Duration)
	scalar("oci_capacity_collection_errors", "Failed queries in latest scan.", len(s.Errors))
	var counts, available, statuses, states, checked, failures strings.Builder
	writeState := func(q query, fd, status string, at time.Time, state int) {
		labels := metricLabels(q, fd)
		fmt.Fprintf(&states, "oci_capacity_state{%s} %d\n", labels, state)
		fmt.Fprintf(&checked, "oci_capacity_checked_timestamp_seconds{%s} %v\n", labels, timestamp(at))
		fmt.Fprintf(&statuses, "oci_capacity_status{%s,status=%s} 1\n", labels, strconv.Quote(status))
	}
	// Successful observations remain usable even when another target fails.
	for _, r := range s.Results {
		for _, a := range r.Report.ShapeAvailabilities {
			fd := ""
			if a.FaultDomain != nil {
				fd = *a.FaultDomain
			}
			labels := metricLabels(r.Query, fd)
			if a.AvailableCount != nil {
				fmt.Fprintf(&counts, "oci_capacity_available_count{%s} %d\n", labels, *a.AvailableCount)
			}
			v, state := 0, 0
			switch string(a.AvailabilityStatus) {
			case "AVAILABLE":
				v, state = 1, 1
			case "OUT_OF_HOST_CAPACITY":
				state = 2
			case "HARDWARE_NOT_SUPPORTED":
				state = 3
			}
			fmt.Fprintf(&available, "oci_capacity_available{%s} %d\n", labels, v)
			writeState(r.Query, fd, string(a.AvailabilityStatus), r.CheckedAt, state)
		}
	}
	for _, f := range s.Failures {
		writeState(f.Query, f.Query.FaultDomain, "UNKNOWN", f.CheckedAt, 0)
		// Error messages stay in JSON/logs; a bounded stage label avoids cardinality growth.
		fmt.Fprintf(&failures, "oci_capacity_check_failure{%s,stage=%s} 1\n", metricLabels(f.Query, f.Query.FaultDomain), strconv.Quote(f.Stage))
	}
	gauge("oci_capacity_available_count", "Reported launchable instance count for the requested shape configuration; absent when unknown.")
	b.WriteString(counts.String())
	gauge("oci_capacity_available", "One if OCI reports AVAILABLE; absent for failed checks.")
	b.WriteString(available.String())
	gauge("oci_capacity_status", "Latest observed status or UNKNOWN after a failed check, with value one.")
	b.WriteString(statuses.String())
	gauge("oci_capacity_state", "Latest check: 0 unknown, 1 available, 2 out of capacity, 3 unsupported. Check observation age before deployment.")
	b.WriteString(states.String())
	gauge("oci_capacity_checked_timestamp_seconds", "Completion time of each individual capacity check or discovery failure.")
	b.WriteString(checked.String())
	gauge("oci_capacity_check_failure", "One for failed checks, labelled by discovery or capacity stage.")
	b.WriteString(failures.String())
	return b.String()
}
