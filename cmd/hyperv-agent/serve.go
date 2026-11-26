/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/agent"
	pb "github.com/klotho-k8s/cluster-api-provider-hyperv/proto/hypervagent/v1"
)

// tokenEnv is the environment variable the agent reads its bearer token from.
// The same token is shared with the controller (as a Kubernetes Secret).
const tokenEnv = "HYPERV_AGENT_TOKEN"

// defaultPort is the agent's default TLS gRPC port. It deliberately avoids 5986
// (WinRM-over-HTTPS), which is commonly already bound on a Hyper-V host.
const defaultPort = "7443"

// serveConfig is the resolved runtime configuration of the agent server.
type serveConfig struct {
	listen    string   // host:port to bind; empty host => a non-loopback IP is chosen and logged
	token     string   // bearer token (from env); if empty, tokenFile is read
	tokenFile string   // file holding the bearer token (locked-down; used by the service)
	certFile  string   // TLS cert; empty => self-signed into dataDir
	keyFile   string   // TLS key; empty => self-signed into dataDir
	dataDir   string   // where a generated self-signed cert/key and cidata ISOs live
	tlsHosts  []string // extra SANs to add to the self-signed cert (--tls-hosts)
	sans      []string // resolved SAN list (set by resolve)
	backend   string   // Hyper-V backend: "wmi" (default) or "powershell"
}

// isoDir is where the agent's PowerShell client writes cidata ISOs on the host.
func (c serveConfig) isoDir() string { return filepath.Join(c.dataDir, "cidata") }

// vmsDir is where the agent's PowerShell client writes each VM's own boot disk
// (a copy of the base image) on the host.
func (c serveConfig) vmsDir() string { return filepath.Join(c.dataDir, "vms") }

// bearerToken returns the configured token, reading tokenFile when no token came
// from the environment.
func (c serveConfig) bearerToken() (string, error) {
	if c.token != "" {
		return c.token, nil
	}
	if c.tokenFile != "" {
		b, err := os.ReadFile(c.tokenFile)
		if err != nil {
			return "", fmt.Errorf("agent: read token file: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", nil
}

// resolve fixes the concrete bind address and the TLS SAN list, logging the
// chosen bind. When no listen host is given it picks the first non-loopback IPv4
// (so the agent does not silently listen on every interface); an explicit
// 0.0.0.0 is honored but warned about, and the cert then covers the hostname and
// all non-loopback IPs so the controller can verify by name. The SANs always
// include localhost/127.0.0.1 and any --tls-hosts.
func (c *serveConfig) resolve(logf func(string, ...any)) {
	host, port, err := net.SplitHostPort(c.listen)
	if err != nil {
		// No port (or malformed): treat the whole value as the host.
		host, port = c.listen, ""
	}
	if port == "" {
		port = defaultPort
	}

	sans := append([]string{"localhost", "127.0.0.1"}, c.tlsHosts...)
	if hn, herr := os.Hostname(); herr == nil && hn != "" {
		sans = append(sans, hn)
	}

	switch host {
	case "":
		if ip := firstNonLoopbackIPv4(); ip != "" {
			host = ip
			logf("no --listen host given; binding to %s:%s (pass --listen to override)", host, port)
		} else {
			host = "0.0.0.0"
			logf("no --listen host given and no non-loopback address found; binding to all interfaces on port %s", port)
		}
		sans = append(sans, nonLoopbackIPv4s()...)
	case "0.0.0.0", "::":
		logf("WARNING: binding to all interfaces (%s); the agent controls VMs, prefer --listen <host-ip>:%s", c.listen, port)
		sans = append(sans, nonLoopbackIPv4s()...)
	default:
		sans = append(sans, host)
	}

	c.listen = net.JoinHostPort(host, port)
	c.sans = dedupeStrings(sans)
}

// resolveTLS returns the cert and key paths to serve with, generating a
// self-signed pair (covering c.sans) into dataDir on first run when none were
// provided. resolve must have run first.
func (c serveConfig) resolveTLS() (certFile, keyFile string, err error) {
	if c.certFile != "" && c.keyFile != "" {
		return c.certFile, c.keyFile, nil
	}
	cert := filepath.Join(c.dataDir, "agent.crt")
	key := filepath.Join(c.dataDir, "agent.key")
	if fileExists(cert) && fileExists(key) {
		return cert, key, nil
	}
	if err := os.MkdirAll(c.dataDir, 0o700); err != nil {
		return "", "", fmt.Errorf("agent: data dir: %w", err)
	}
	hosts := c.sans
	if len(hosts) == 0 {
		hosts = []string{"localhost", "127.0.0.1"}
	}
	certPEM, keyPEM, err := agent.GenerateSelfSigned(hosts, 10*365*24*time.Hour)
	if err != nil {
		return "", "", err
	}
	if err := agent.WriteKeyPair(cert, key, certPEM, keyPEM); err != nil {
		return "", "", err
	}
	return cert, key, nil
}

// firstNonLoopbackIPv4 returns the first non-loopback IPv4 address of the host,
// or "" if none can be determined.
func firstNonLoopbackIPv4() string {
	if ips := nonLoopbackIPv4s(); len(ips) > 0 {
		return ips[0]
	}
	return ""
}

// nonLoopbackIPv4s returns the host's non-loopback IPv4 addresses.
func nonLoopbackIPv4s() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			out = append(out, v4.String())
		}
	}
	return out
}

// dedupeStrings returns xs with duplicates and empties removed, order preserved.
func dedupeStrings(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// buildServer wires the gRPC server: TLS, the token interceptor, and the
// Hyper-V-backed service. The backing client is the PowerShell implementation,
// so the agent is the only component that runs PowerShell. resolve must have run
// on cfg first (so the SAN list and bind address are fixed).
func buildServer(cfg serveConfig) (*grpc.Server, error) {
	token, err := cfg.bearerToken()
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New("agent: a bearer token is required (set " + tokenEnv + " or --token-file)")
	}
	certFile, keyFile, err := cfg.resolveTLS()
	if err != nil {
		return nil, err
	}
	tlsOpt, err := agent.ServerTLS(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	client, err := newHostClient(cfg)
	if err != nil {
		return nil, err
	}
	srv := grpc.NewServer(tlsOpt, grpc.UnaryInterceptor(agent.TokenAuthInterceptor(token)))
	pb.RegisterHyperVAgentServer(srv, agent.NewServer(client))
	return srv, nil
}

// serve runs the agent until ctx is cancelled (or the listener fails).
func serve(ctx context.Context, cfg serveConfig, logf func(string, ...any)) error {
	cfg.resolve(logf)
	srv, err := buildServer(cfg)
	if err != nil {
		return err
	}
	lis, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return fmt.Errorf("agent: listen %s: %w", cfg.listen, err)
	}
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	logf("hyperv-agent serving on %s (TLS, token auth)", cfg.listen)
	if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return fmt.Errorf("agent: serve: %w", err)
	}
	return nil
}
