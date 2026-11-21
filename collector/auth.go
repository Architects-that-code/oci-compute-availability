package main

import (
	"fmt"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
)

type authOptions struct {
	mode, configFile, profile string
	once                      bool
}

func (o authOptions) validate() error {
	switch o.mode {
	case "workload", "instance", "resource":
		if o.configFile != "" || o.profile != "DEFAULT" {
			return fmt.Errorf("config-file and profile require auth=config")
		}
	case "config":
		if !o.once || o.configFile == "" {
			return fmt.Errorf("auth=config requires --once and an explicit --config-file for local validation")
		}
	default:
		return fmt.Errorf("auth must be workload, instance, resource, or config")
	}
	return nil
}

func (o authOptions) provider() (common.ConfigurationProvider, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	switch o.mode {
	case "workload":
		return auth.OkeWorkloadIdentityConfigurationProvider()
	case "instance":
		return auth.InstancePrincipalConfigurationProvider()
	case "resource":
		return auth.ResourcePrincipalConfigurationProvider()
	case "config":
		return common.ConfigurationProviderFromFileWithProfile(o.configFile, o.profile, "")
	}
	return nil, fmt.Errorf("unsupported auth mode")
}
