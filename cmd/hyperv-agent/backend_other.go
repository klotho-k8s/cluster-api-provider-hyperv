//go:build !windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"fmt"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
)

// newHostClient builds the Hyper-V Client the agent serves with. Off Windows the
// WMI backend does not compile (microsoft/wmi is Windows-only), so only the
// PowerShell client is available; an explicit --backend=wmi is an error here
// rather than a silent substitution. The agent is meant to run on a Hyper-V
// host; this path exists so the command still builds for the Linux controller
// image.
func newHostClient(cfg serveConfig) (hyperv.Client, error) {
	backend, err := hyperv.ParseBackend(cfg.backend)
	if err != nil {
		return nil, err
	}
	if backend == hyperv.BackendWMI && cfg.backend != "" {
		return nil, fmt.Errorf("the %q backend is only available on Windows; use --backend=%s", hyperv.BackendWMI, hyperv.BackendPowerShell)
	}
	return hyperv.NewPowerShellClient(cfg.isoDir(), cfg.vmsDir()), nil
}
