/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

// Command manager runs the Cluster API infrastructure provider for Microsoft
// Hyper-V.
//
// Unusually for a Cluster API provider, this controller must be able to reach a
// Hyper-V host and run its PowerShell management cmdlets. It is intended to run
// ON the Windows Hyper-V host (or on a host with the Hyper-V PowerShell module
// and rights to manage the target host), not purely in-cluster. See the README.
package main

import (
	"flag"
	"os"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/flags"
	"sigs.k8s.io/cluster-api/util/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	// Import all Kubernetes client auth plugins to make exec-entrypoint and run
	// able to use them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	infrav1 "github.com/klotho-k8s/cluster-api-provider-hyperv/api/v1alpha1"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	enableLeaderElection bool
	probeAddr            string
	isoDir               string
	managerOptions       = flags.ManagerOptions{}
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)
	_ = infrav1.AddToScheme(scheme)
}

func main() {
	ctrl.SetLogger(klog.Background())
	klog.InitFlags(nil)

	fs := pflag.CommandLine
	fs.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	fs.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. Ensures there is only one active controller manager.")
	fs.StringVar(&isoDir, "iso-dir", defaultISODir(),
		"Directory where generated cloud-init cidata ISOs are written (local mode only; in agent mode the host agent writes them under its own data dir).")
	var agentEndpoint, agentCA string
	var agentInsecure bool
	fs.StringVar(&agentEndpoint, "hyperv-agent-endpoint", os.Getenv("HYPERV_AGENT_ENDPOINT"), "gRPC endpoint of a Hyper-V host agent (host:port); when set, the controller drives Hyper-V through the agent instead of local PowerShell")
	fs.StringVar(&agentCA, "hyperv-agent-ca", "", "CA certificate file to trust for the agent TLS connection (system roots if empty)")
	fs.BoolVar(&agentInsecure, "hyperv-agent-insecure", false, "disable TLS to the agent (development only)")
	flags.AddManagerOptions(fs, &managerOptions)
	fs.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	_, metricsOptions, err := flags.GetManagerOptions(managerOptions)
	if err != nil {
		setupLog.Error(err, "unable to start manager: invalid flags")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                *metricsOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "controller-leader-elect-capi-hyperv",
		LeaseDuration:          durationPtr(15 * time.Second),
		RenewDeadline:          durationPtr(10 * time.Second),
		RetryPeriod:            durationPtr(2 * time.Second),
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	record.InitFromRecorder(mgr.GetEventRecorderFor("hyperv-controller"))

	ctx := ctrl.SetupSignalHandler()

	// The production Hyper-V backend shells out to powershell.exe. On a non-
	// Windows host this still builds; it only fails at the point cmdlets run.
	hyperVClient, err := newHyperVClient(agentEndpoint, agentCA, os.Getenv("HYPERV_AGENT_CA"), os.Getenv("HYPERV_AGENT_TOKEN"), agentInsecure, isoDir)
	if err != nil {
		setupLog.Error(err, "unable to build the Hyper-V client")
		os.Exit(1)
	}

	if err := (&controller.HyperVClusterReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("hypervcluster-controller"),
	}).SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HyperVCluster")
		os.Exit(1)
	}
	if err := (&controller.HyperVMachineReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Recorder:     mgr.GetEventRecorderFor("hypervmachine-controller"),
		HyperVClient: hyperVClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "HyperVMachine")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func durationPtr(d time.Duration) *time.Duration { return &d }

// defaultISODir returns a sensible default host directory for cidata ISOs.
func defaultISODir() string {
	if dir := os.Getenv("HYPERV_ISO_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("ProgramData"); dir != "" {
		return dir + `\cluster-api-provider-hyperv\cidata`
	}
	return os.TempDir()
}
