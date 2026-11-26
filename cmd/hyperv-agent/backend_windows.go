//go:build windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
)

// newHostClient builds the Hyper-V Client the agent serves with. On Windows both
// backends are available: WMI (the default, native Msvm_* calls) and PowerShell
// (the fallback). The choice comes from cfg.backend, already validated.
func newHostClient(cfg serveConfig) (hyperv.Client, error) {
	backend, err := hyperv.ParseBackend(cfg.backend)
	if err != nil {
		return nil, err
	}
	switch backend {
	case hyperv.BackendPowerShell:
		return hyperv.NewPowerShellClient(cfg.isoDir(), cfg.vmsDir()), nil
	default:
		return hyperv.NewWMIClient(cfg.isoDir(), cfg.vmsDir()), nil
	}
}
