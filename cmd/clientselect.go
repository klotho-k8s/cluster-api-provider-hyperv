/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"fmt"
	"path/filepath"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/agent"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
)

// newHyperVClient selects the Hyper-V backend. With no agent endpoint it is the
// local PowerShell client (the controller runs on the Hyper-V host, writing
// cidata ISOs under isoDir). With an endpoint it dials a remote Hyper-V host
// agent over gRPC, so the controller runs anywhere (a Linux pod) and executes
// no PowerShell. TLS trusts caFile (system roots if empty); token is the bearer
// token shared with the agent. insecureTLS disables transport security for
// local development only.
func newHyperVClient(endpoint, caFile, token string, insecureTLS bool, isoDir string) (hyperv.Client, error) {
	if endpoint == "" {
		// Local mode: put each VM's own boot disk beside the cidata dir.
		vmsDir := filepath.Join(filepath.Dir(isoDir), "vms")
		return hyperv.NewPowerShellClient(isoDir, vmsDir), nil
	}
	if token == "" {
		return nil, fmt.Errorf("an agent endpoint (%s) is set but no token was provided; set HYPERV_AGENT_TOKEN", endpoint)
	}
	var transport grpc.DialOption
	if insecureTLS {
		transport = grpc.WithTransportCredentials(insecure.NewCredentials())
	} else {
		var err error
		if transport, err = agent.ClientTLS(caFile, ""); err != nil {
			return nil, err
		}
	}
	return agent.Dial(endpoint, transport, agent.TokenCredentials(token, !insecureTLS))
}
