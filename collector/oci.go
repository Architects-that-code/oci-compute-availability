package main

import (
	"context"
	"fmt"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/identity"
	"net/http"
	"sort"
	"time"
)

type query struct {
	Name               string                                  `json:"name"`
	Region             string                                  `json:"region,omitempty"`
	AvailabilityDomain string                                  `json:"availabilityDomain,omitempty"`
	Shape              string                                  `json:"shape"`
	FaultDomain        string                                  `json:"faultDomain,omitempty"`
	ShapeConfig        *core.CapacityReportInstanceShapeConfig `json:"shapeConfig,omitempty"`
}

type reporter interface {
	Report(context.Context, query) (core.ComputeCapacityReport, error)
	AvailabilityDomains(context.Context, string) ([]string, error)
	SubscribedRegions(context.Context) ([]string, error)
}
type ociReporter struct {
	provider common.ConfigurationProvider
	tenancy  string
	clients  map[string]core.ComputeClient
}

func (r *ociReporter) SubscribedRegions(ctx context.Context) ([]string, error) {
	c, err := identity.NewIdentityClientWithConfigurationProvider(r.provider)
	if err != nil {
		return nil, err
	}
	c.HTTPClient = &http.Client{Timeout: 45 * time.Second}
	retry := common.DefaultRetryPolicy()
	retry.MaximumNumberAttempts = 4
	response, err := c.ListRegionSubscriptions(ctx, identity.ListRegionSubscriptionsRequest{
		TenancyId: &r.tenancy, RequestMetadata: common.RequestMetadata{RetryPolicy: &retry},
	})
	if err != nil {
		return nil, err
	}
	var names []string
	seen := map[string]bool{}
	for _, subscription := range response.Items {
		if subscription.RegionName == nil || *subscription.RegionName == "" {
			return nil, fmt.Errorf("missing subscribed region name")
		}
		if !seen[*subscription.RegionName] {
			names = append(names, *subscription.RegionName)
			seen[*subscription.RegionName] = true
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no subscribed regions returned")
	}
	sort.Strings(names)
	return names, nil
}

func (r *ociReporter) AvailabilityDomains(ctx context.Context, region string) ([]string, error) {
	c, err := identity.NewIdentityClientWithConfigurationProvider(r.provider)
	if err != nil {
		return nil, err
	}
	c.SetRegion(region)
	c.HTTPClient = &http.Client{Timeout: 45 * time.Second}
	retry := common.DefaultRetryPolicy()
	retry.MaximumNumberAttempts = 4
	response, err := c.ListAvailabilityDomains(ctx, identity.ListAvailabilityDomainsRequest{
		CompartmentId: &r.tenancy, RequestMetadata: common.RequestMetadata{RetryPolicy: &retry},
	})
	if err != nil {
		return nil, err
	}
	if response.OpcNextPage != nil && *response.OpcNextPage != "" {
		return nil, fmt.Errorf("incomplete availability-domain list in %s", region)
	}
	var names []string
	seen := map[string]bool{}
	for _, ad := range response.Items {
		if ad.Name == nil || *ad.Name == "" {
			return nil, fmt.Errorf("missing availability-domain name in %s", region)
		}
		if !seen[*ad.Name] {
			names = append(names, *ad.Name)
			seen[*ad.Name] = true
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no availability domains returned for %s", region)
	}
	sort.Strings(names)
	return names, nil
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
