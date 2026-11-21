package main

import (
	"context"
	"fmt"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"net/http"
	"time"
)

type query struct {
	Name               string                                  `json:"name"`
	Region             string                                  `json:"region"`
	AvailabilityDomain string                                  `json:"availabilityDomain"`
	Shape              string                                  `json:"shape"`
	FaultDomain        string                                  `json:"faultDomain,omitempty"`
	ShapeConfig        *core.CapacityReportInstanceShapeConfig `json:"shapeConfig,omitempty"`
}

type reporter interface {
	Report(context.Context, query) (core.ComputeCapacityReport, error)
}
type ociReporter struct {
	provider common.ConfigurationProvider
	tenancy  string
	clients  map[string]core.ComputeClient
}

// Collection is sequential, so regional clients need no concurrent mutation.
func (r *ociReporter) Report(ctx context.Context, q query) (core.ComputeCapacityReport, error) {
	c, ok := r.clients[q.Region]
	if !ok {
		var err error
		c, err = core.NewComputeClientWithConfigurationProvider(r.provider)
		if err != nil {
			return core.ComputeCapacityReport{}, err
		}
		c.SetRegion(q.Region)
		c.HTTPClient = &http.Client{Timeout: 45 * time.Second}
		r.clients[q.Region] = c
	}
	shape := core.CreateCapacityReportShapeAvailabilityDetails{InstanceShape: &q.Shape, InstanceShapeConfig: q.ShapeConfig}
	if q.FaultDomain != "" {
		shape.FaultDomain = &q.FaultDomain
	}
	retry := common.DefaultRetryPolicy()
	retry.MaximumNumberAttempts = 4
	response, err := c.CreateComputeCapacityReport(ctx, core.CreateComputeCapacityReportRequest{
		CreateComputeCapacityReportDetails: core.CreateComputeCapacityReportDetails{
			CompartmentId: &r.tenancy, AvailabilityDomain: &q.AvailabilityDomain,
			ShapeAvailabilities: []core.CreateCapacityReportShapeAvailabilityDetails{shape},
		}, RequestMetadata: common.RequestMetadata{RetryPolicy: &retry},
	})
	if err != nil {
		return core.ComputeCapacityReport{}, fmt.Errorf("query %s: %w", q.Name, err)
	}
	return response.ComputeCapacityReport, nil
}
