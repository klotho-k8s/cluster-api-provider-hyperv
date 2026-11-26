//go:build windows

/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
)

// errorServiceExists is ERROR_SERVICE_EXISTS (Win32 1073): sc.exe returns it
// (and prints it) when asked to create a service that already exists.
const errorServiceExists = 1073

// serviceDisplayName is the human-readable name shown in services.msc.
const serviceDisplayName = "Hyper-V Agent (Cluster API)"

// serviceDependencies are the services that must be running before the SCM
// starts the agent: vmms is the Hyper-V Virtual Machine Management service the
// agent drives; Tcpip backs its gRPC listener. sc.exe depend= is '/'-separated.
const serviceDependencies = "vmms/Tcpip"

// tokenFileName is the file under the data dir that holds the bearer token. The
// data dir is locked to SYSTEM+Administrators (secureDACL), so the token is not
// readable by unprivileged local users. It is passed to the service via
// --token-file, never placed on the service command line or in the (Users-
// readable) service registry Environment value.
const tokenFileName = "token"

// serviceBinPath composes the service ImagePath: the agent exe plus the run
// subcommand and its flags. This string is what the SCM launches. The token is
// delivered by --token-file, not here.
func serviceBinPath(bin, listen, dataDir, tokenFile string, tlsHosts []string, backend string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `"%s" run --data-dir "%s" --token-file "%s"`, bin, dataDir, tokenFile)
	if listen != "" {
		fmt.Fprintf(&b, ` --listen "%s"`, listen)
	}
	for _, h := range tlsHosts {
		fmt.Fprintf(&b, ` --tls-hosts "%s"`, h)
	}
	if backend != "" {
		fmt.Fprintf(&b, ` --backend "%s"`, backend)
	}
	return b.String()
}

// createServiceArgs is the sc.exe argument list that registers the service
// fresh.
func createServiceArgs(bin, listen, dataDir, tokenFile string, tlsHosts []string, backend string) []string {
	return []string{
		"create", serviceName,
		"binPath=", serviceBinPath(bin, listen, dataDir, tokenFile, tlsHosts, backend),
		"start=", "delayed-auto",
		"depend=", serviceDependencies,
		"DisplayName=", serviceDisplayName,
	}
}

// configServiceArgs converges an existing service onto the same definition
// createServiceArgs would have written.
func configServiceArgs(bin, listen, dataDir, tokenFile string, tlsHosts []string, backend string) []string {
	return []string{
		"config", serviceName,
		"binPath=", serviceBinPath(bin, listen, dataDir, tokenFile, tlsHosts, backend),
		"start=", "delayed-auto",
		"depend=", serviceDependencies,
	}
}

// writeTokenFile writes the bearer token into the (locked-down) data dir and
// returns its path. The directory is created and its DACL restricted first, so
// the token file inherits SYSTEM+Administrators-only access.
func writeTokenFile(dataDir, token string) (string, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create data dir: %w", err)
	}
	if err := secureDACL(dataDir); err != nil {
		return "", fmt.Errorf("secure data dir: %w", err)
	}
	path := filepath.Join(dataDir, tokenFileName)
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}
	// Belt and braces: pin the token file's own DACL too, in case the directory
	// already existed with inheritance that did not propagate to this write.
	if err := secureDACL(path); err != nil {
		return "", fmt.Errorf("secure token file: %w", err)
	}
	return path, nil
}

func sc(args ...string) (string, error) {
	out, err := exec.Command("sc.exe", args...).CombinedOutput()
	return string(out), err
}

// serviceState returns the current state word from `sc query` (for example
// "RUNNING", "STOPPED"), or "" if it cannot be determined.
func serviceState() string {
	out, _ := sc("query", serviceName)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "STATE") {
			f := strings.Fields(line)
			if len(f) > 0 {
				return f[len(f)-1]
			}
		}
	}
	return ""
}

// stopAndWait stops the service and waits until it reports STOPPED (or the
// timeout elapses), so a following start picks up a changed binPath/token.
func stopAndWait(timeout time.Duration) {
	if out, err := sc("stop", serviceName); err != nil && !strings.Contains(out, "1062") {
		// 1062 = not started; nothing to wait for.
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if serviceState() == "STOPPED" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	var listen, dataDir, bin, token, backend string
	var tlsHosts stringSlice
	fs.StringVar(&listen, "listen", os.Getenv("HYPERV_AGENT_LISTEN"), "host:port to bind (empty host picks a non-loopback IP)")
	fs.StringVar(&dataDir, "data-dir", defaultDataDir(), "directory for the token, self-signed cert/key and cidata ISOs")
	fs.StringVar(&bin, "bin", "", "path to the agent exe (default: this exe)")
	fs.StringVar(&token, "token", os.Getenv(tokenEnv), "bearer token (default: $"+tokenEnv+")")
	fs.Var(&tlsHosts, "tls-hosts", "extra DNS name or IP to add to the self-signed cert SANs (repeatable)")
	fs.StringVar(&backend, "backend", os.Getenv("HYPERV_AGENT_BACKEND"), "Hyper-V backend: wmi (default) or powershell")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("a token is required: pass --token or set %s", tokenEnv)
	}
	// Validate the backend now so a typo fails install instead of crash-looping
	// the service later.
	if _, err := hyperv.ParseBackend(backend); err != nil {
		return err
	}
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve exe: %w", err)
		}
		bin = exe
	}

	// Store the token in the locked-down data dir and point the service at it.
	tokenFile, err := writeTokenFile(dataDir, token)
	if err != nil {
		return err
	}

	existed := false
	out, err := sc(createServiceArgs(bin, listen, dataDir, tokenFile, tlsHosts, backend)...)
	switch {
	case err == nil:
		// created
	case strings.Contains(out, fmt.Sprint(errorServiceExists)) || exitCode(err) == errorServiceExists:
		existed = true
		if out, err := sc(configServiceArgs(bin, listen, dataDir, tokenFile, tlsHosts, backend)...); err != nil {
			return fmt.Errorf("sc config: %v: %s", err, out)
		}
	default:
		return fmt.Errorf("sc create: %v: %s", err, out)
	}

	// A running service keeps its old binPath/token until restarted, so a
	// reinstall (token rotation, flag change) must stop it first; sc start then
	// never returns 1056 (already running).
	if existed {
		stopAndWait(30 * time.Second)
	}
	if out, err := sc("start", serviceName); err != nil && !strings.Contains(out, "1056") {
		// 1056 = already running; otherwise report
		return fmt.Errorf("sc start: %v: %s", err, out)
	}
	fmt.Printf("installed and started %q (listen %q, token in %s)\n", serviceName, listenOrAuto(listen), tokenFile)
	return nil
}

// listenOrAuto renders the listen value for the install summary.
func listenOrAuto(listen string) string {
	if listen == "" {
		return "auto (non-loopback IP:" + defaultPort + ")"
	}
	return listen
}

func cmdUninstall([]string) error {
	stopAndWait(30 * time.Second)
	if out, err := sc("delete", serviceName); err != nil {
		if strings.Contains(out, "1072") {
			// 1072 = marked for deletion: a prior stop/delete is still settling.
			return fmt.Errorf("service %q is marked for deletion; wait a moment and retry: %s", serviceName, strings.TrimSpace(out))
		}
		return fmt.Errorf("sc delete: %v: %s", err, out)
	}
	fmt.Printf("removed %q\n", serviceName)
	return nil
}

// exitCode returns the process exit code from an exec error, or -1.
func exitCode(err error) int {
	var ee *exec.ExitError
	if ok := asExit(err, &ee); ok {
		return ee.ExitCode()
	}
	return -1
}
