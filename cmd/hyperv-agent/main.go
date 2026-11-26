/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

// Command hyperv-agent is the Hyper-V host agent. It runs as a Windows service
// on a Hyper-V host and exposes the Hyper-V VM lifecycle over an authenticated,
// TLS gRPC API. The Cluster API Hyper-V controller (a Linux pod in the workload
// cluster) drives Hyper-V through this agent, so the controller never executes
// PowerShell. All PowerShell/WMI work happens here, on the host.
//
// The bearer token is read from the HYPERV_AGENT_TOKEN environment variable and
// must match the token the controller presents (shared as a Kubernetes Secret).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// serviceName is the Windows service name registered by install.
const serviceName = "hyperv-agent"

// version is stamped at build time via -ldflags.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	if err := dispatch(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "hyperv-agent:", err)
		os.Exit(1)
	}
}

func dispatch(cmd string, args []string) error {
	switch cmd {
	case "run":
		return cmdRun(args)
	case "install":
		return cmdInstall(args)
	case "uninstall":
		return cmdUninstall(args)
	case "version", "--version", "-v":
		fmt.Println("hyperv-agent", version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `hyperv-agent - Hyper-V host agent (gRPC) for the Cluster API Hyper-V provider

Commands:
  run         Run the agent in the foreground (or under the Windows SCM).
  install     Install and start the Windows service (sc.exe).
  uninstall   Stop and remove the Windows service.
  version     Print the version.

Flags (run/install):
  --listen     host:port to bind (default port 7443; empty host picks a non-loopback IP).
  --data-dir   directory for the self-signed cert/key and cidata ISOs (default alongside the exe).
  --tls-cert   TLS certificate file (optional; self-signed if unset).
  --tls-key    TLS key file (optional; self-signed if unset).
  --tls-hosts  extra DNS name/IP for the self-signed cert SANs (repeatable).
  --token-file file holding the bearer token (used when HYPERV_AGENT_TOKEN is unset).
  --backend    Hyper-V backend: wmi (default, native WMI) or powershell (fallback).
  --bin        (install) path to the agent exe to register (default: this exe).

Environment:
  HYPERV_AGENT_TOKEN    bearer token required on every RPC (share with the controller).
  HYPERV_AGENT_BACKEND  default backend when --backend is unset (wmi or powershell).
`)
}

// defaultDataDir is the directory next to the executable where a generated
// self-signed cert/key live by default.
func defaultDataDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Join(filepath.Dir(exe), "data")
}

// stringSlice is a repeatable string flag (e.g. --tls-hosts a --tls-hosts b).
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// runFlags parses the shared run/serve flags.
func runFlags(args []string) serveConfig {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	cfg := serveConfig{token: os.Getenv(tokenEnv)}
	var tlsHosts stringSlice
	fs.StringVar(&cfg.listen, "listen", os.Getenv("HYPERV_AGENT_LISTEN"), "host:port to bind (empty host picks a non-loopback IP)")
	fs.StringVar(&cfg.dataDir, "data-dir", defaultDataDir(), "directory for the self-signed cert/key and cidata ISOs")
	fs.StringVar(&cfg.certFile, "tls-cert", "", "TLS certificate file")
	fs.StringVar(&cfg.keyFile, "tls-key", "", "TLS key file")
	fs.StringVar(&cfg.tokenFile, "token-file", os.Getenv("HYPERV_AGENT_TOKEN_FILE"), "file holding the bearer token (used when HYPERV_AGENT_TOKEN is unset)")
	fs.Var(&tlsHosts, "tls-hosts", "extra DNS name or IP to add to the self-signed cert SANs (repeatable)")
	fs.StringVar(&cfg.backend, "backend", os.Getenv("HYPERV_AGENT_BACKEND"), "Hyper-V backend: wmi (default) or powershell")
	_ = fs.Parse(args)
	cfg.tlsHosts = tlsHosts
	return cfg
}

// cmdRun runs the agent, dispatching to the Windows SCM when launched as a
// service and to the foreground otherwise (runAgent is OS-specific).
func cmdRun(args []string) error {
	return runAgent(runFlags(args))
}

// runForeground runs the server until an interrupt signal. It is the
// non-service run path, shared by every OS.
func runForeground(cfg serveConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, cfg, func(f string, a ...any) { fmt.Printf(f+"\n", a...) })
}
