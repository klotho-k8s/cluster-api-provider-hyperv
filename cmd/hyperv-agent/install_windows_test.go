//go:build windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"slices"
	"strings"
	"testing"
)

func TestServiceBinPath(t *testing.T) {
	got := serviceBinPath(
		`C:\Program Files\klotho\hyperv-agent.exe`, "192.168.1.5:7443",
		`C:\ProgramData\hyperv-agent`, `C:\ProgramData\hyperv-agent\token`,
		[]string{"host.example"}, "",
	)
	want := `"C:\Program Files\klotho\hyperv-agent.exe" run --data-dir "C:\ProgramData\hyperv-agent" --token-file "C:\ProgramData\hyperv-agent\token" --listen "192.168.1.5:7443" --tls-hosts "host.example"`
	if got != want {
		t.Fatalf("binPath\n got %q\nwant %q", got, want)
	}
}

// TestServiceBinPathBackend checks an explicit backend is forwarded to the
// service command line, and that the default (empty) omits the flag so the run
// command's own default (WMI) applies.
func TestServiceBinPathBackend(t *testing.T) {
	with := serviceBinPath(`C:\a.exe`, "", `C:\d`, `C:\d\token`, nil, "powershell")
	if !strings.Contains(with, `--backend "powershell"`) {
		t.Fatalf("explicit backend should be forwarded: %q", with)
	}
	without := serviceBinPath(`C:\a.exe`, "", `C:\d`, `C:\d\token`, nil, "")
	if strings.Contains(without, "--backend") {
		t.Fatalf("empty backend should not add --backend: %q", without)
	}
}

// TestServiceBinPathOmitsEmptyListen checks the --listen flag is dropped when no
// listen address is configured (so the agent auto-selects one at runtime).
func TestServiceBinPathOmitsEmptyListen(t *testing.T) {
	got := serviceBinPath(`C:\a.exe`, "", `C:\d`, `C:\d\token`, nil, "")
	if strings.Contains(got, "--listen") {
		t.Fatalf("empty listen should not add --listen: %q", got)
	}
}

// TestServiceBinPathHasNoToken is the H1 regression guard: the bearer token is
// never placed on the service command line, only a path to the locked-down file.
func TestServiceBinPathHasNoToken(t *testing.T) {
	got := serviceBinPath(`C:\a.exe`, ":7443", `C:\d`, `C:\d\token`, nil, "")
	if !strings.Contains(got, `--token-file "C:\d\token"`) {
		t.Fatalf("binPath must pass --token-file: %q", got)
	}
	if strings.Contains(got, "--token ") {
		t.Fatalf("binPath must not carry the raw token: %q", got)
	}
}

func TestCreateServiceArgs(t *testing.T) {
	args := createServiceArgs(`C:\a.exe`, ":7443", `C:\d`, `C:\d\token`, nil, "")
	// sc.exe parses alternating key= / value tokens; verify the key tokens and
	// the fixed values are present and correctly ordered.
	if args[0] != "create" || args[1] != serviceName {
		t.Fatalf("args[0:2] = %v", args[:2])
	}
	for _, kv := range [][2]string{
		{"start=", "delayed-auto"},
		{"depend=", serviceDependencies},
		{"DisplayName=", serviceDisplayName},
	} {
		i := slices.Index(args, kv[0])
		if i < 0 || i+1 >= len(args) || args[i+1] != kv[1] {
			t.Fatalf("missing/!= %q %q in %v", kv[0], kv[1], args)
		}
	}
	bp := slices.Index(args, "binPath=")
	if bp < 0 || !strings.Contains(args[bp+1], "run --data-dir") {
		t.Fatalf("binPath not wired: %v", args)
	}
}

func TestConfigServiceArgs(t *testing.T) {
	args := configServiceArgs(`C:\a.exe`, ":7443", `C:\d`, `C:\d\token`, nil, "")
	if args[0] != "config" || args[1] != serviceName {
		t.Fatalf("config args head = %v", args[:2])
	}
	if slices.Index(args, "DisplayName=") != -1 {
		t.Fatalf("config must not set DisplayName: %v", args)
	}
}

func TestDependenciesIncludeHyperV(t *testing.T) {
	if !strings.Contains(serviceDependencies, "vmms") {
		t.Fatalf("service must depend on vmms (Hyper-V VMMS): %q", serviceDependencies)
	}
}
