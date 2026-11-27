/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/agent"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
)

// newHyperVClient selects the Hyper-V backend. With no agent endpoint it is the
// local PowerShell client (the controller runs on the Hyper-V host, writing
// cidata ISOs under isoDir). With an endpoint it dials a remote Hyper-V host
// agent over gRPC, so the controller runs anywhere (a Linux pod) and executes
// no PowerShell.
//
// The agent CA is resolved in order: caPEMB64, a base64-encoded PEM passed
// inline through the HYPERV_AGENT_CA environment variable (how klotho delivers
// it, via the provider configSecret, since a multi-line PEM cannot survive the
// operator's ${VAR} substitution and a Linux pod has no file to mount it as);
// then caFile, a PEM file on disk (--hyperv-agent-ca, for local use); then the
// host's system roots. token is the bearer token shared with the agent;
// insecureTLS disables transport security for local development only.
func newHyperVClient(endpoint, caFile, caPEMB64, token string, insecureTLS bool, isoDir string) (hyperv.Client, error) {
	if endpoint == "" {
		// Local mode: put each VM's own boot disk beside the cidata dir.
		vmsDir := filepath.Join(filepath.Dir(isoDir), "vms")
		return hyperv.NewPowerShellClient(isoDir, vmsDir), nil
	}
	if token == "" {
		return nil, fmt.Errorf("an agent endpoint (%s) is set but no token was provided; set HYPERV_AGENT_TOKEN", endpoint)
	}
	var transport grpc.DialOption
	switch {
	case insecureTLS:
		transport = grpc.WithTransportCredentials(insecure.NewCredentials())
	case strings.TrimSpace(caPEMB64) != "":
		pem, err := base64.StdEncoding.DecodeString(strings.TrimSpace(caPEMB64))
		if err != nil {
			return nil, fmt.Errorf("decoding HYPERV_AGENT_CA (expected base64-encoded PEM): %w", err)
		}
		if transport, err = agent.ClientTLSFromPEM(pem, ""); err != nil {
			return nil, err
		}
	default:
		var err error
		if transport, err = agent.ClientTLS(caFile, ""); err != nil {
			return nil, err
		}
	}
	return agent.Dial(endpoint, transport, agent.TokenCredentials(token, !insecureTLS))
}
