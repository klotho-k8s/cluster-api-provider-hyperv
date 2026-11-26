/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package vmservice

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/klotho-k8s/cluster-api-provider-hyperv/api/v1alpha1"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv/fake"
)

func newScope(t *testing.T, fc *fake.Client) *Scope {
	t.Helper()
	return &Scope{
		Client:  fc,
		Cluster: &clusterv1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: "cl"}},
		HyperVCluster: &infrav1.HyperVCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "cl"},
		},
		Machine: &clusterv1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "node0"}},
		HyperVMachine: &infrav1.HyperVMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "node0"},
			Spec: infrav1.HyperVMachineSpec{
				Image:      `C:\base\ubuntu.vhdx`,
				SwitchName: "External",
				Generation: 2,
				CPUCount:   2,
				MemoryMiB:  2048,
			},
		},
		BootstrapData: []byte("#cloud-config\n"),
		Logger:        logr.Discard(),
	}
}

func TestVMName(t *testing.T) {
	s := newScope(t, fake.New())
	assert.Equal(t, "node0", s.VMName(), "defaults to owning Machine name")

	s.HyperVMachine.Spec.VMName = "explicit-vm"
	assert.Equal(t, "explicit-vm", s.VMName(), "explicit spec.vmName wins")
}

func TestReconcileVMCreatesWhenAbsent(t *testing.T) {
	fc := fake.New()
	s := newScope(t, fc)

	// First pass: VM absent -> created, but no address yet so not ready.
	vm, err := ReconcileVM(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 1, fc.CreateCalls, "should have created the VM")
	assert.Equal(t, 1, fc.AttachCloudInitCfg, "should have attached the cidata ISO")
	assert.Equal(t, []byte("#cloud-config\n"), fc.LastUserData, "bootstrap user-data must cross to the client")
	assert.Equal(t, 1, fc.FirmwareCfg, "should have configured firmware")
	assert.Equal(t, 1, fc.StartCalls, "should have started the VM")
	require.NotNil(t, s.HyperVMachine.Status.Adopted)
	assert.False(t, *s.HyperVMachine.Status.Adopted, "a created VM is not adopted")
	assert.Equal(t, infrav1.ProviderIDPrefix+vm.ID, s.HyperVMachine.Spec.ProviderID)
	assert.Equal(t, infrav1.VirtualMachineStatePending, s.HyperVMachine.Status.VMState)

	// Second pass once the guest reports an address -> ready, no re-create.
	fc.Addresses[s.VMName()] = []string{"192.168.1.50"}
	_, err = ReconcileVM(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 1, fc.CreateCalls, "must not create a second time")
	assert.Equal(t, infrav1.VirtualMachineStateReady, s.HyperVMachine.Status.VMState)
	require.Len(t, s.HyperVMachine.Status.Addresses, 1)
	assert.Equal(t, "192.168.1.50", s.HyperVMachine.Status.Addresses[0].Address)
	assert.Equal(t, clusterv1.MachineInternalIP, s.HyperVMachine.Status.Addresses[0].Type)
}

func TestReconcileVMAdoptsPreExisting(t *testing.T) {
	fc := fake.New()
	// Simulate an external tool (e.g. klotho) having booted node zero already.
	fc.VMs["node0"] = &hyperv.VM{
		Name:       "node0",
		ID:         "preexisting-guid",
		State:      hyperv.StateRunning,
		Generation: 2,
	}
	fc.Addresses["node0"] = []string{"10.0.0.9"}

	s := newScope(t, fc)
	vm, err := ReconcileVM(context.Background(), s)
	require.NoError(t, err)

	assert.Equal(t, 0, fc.CreateCalls, "must adopt, not create")
	assert.Equal(t, 0, fc.AttachCloudInitCfg, "must not re-provision an adopted VM")
	assert.Equal(t, 0, fc.StartCalls, "adopted VM already running")
	require.NotNil(t, s.HyperVMachine.Status.Adopted)
	assert.True(t, *s.HyperVMachine.Status.Adopted, "pre-existing VM must be marked adopted")
	assert.Equal(t, "hyperv://preexisting-guid", s.HyperVMachine.Spec.ProviderID)
	assert.Equal(t, infrav1.VirtualMachineStateReady, s.HyperVMachine.Status.VMState)
	assert.Equal(t, "preexisting-guid", vm.ID)
}

func TestReconcileVMAdoptsByExplicitName(t *testing.T) {
	fc := fake.New()
	fc.VMs["bootstrap-0"] = &hyperv.VM{Name: "bootstrap-0", ID: "g", State: hyperv.StateRunning, Generation: 2}
	fc.Addresses["bootstrap-0"] = []string{"10.0.0.1"}

	s := newScope(t, fc)
	s.HyperVMachine.Spec.VMName = "bootstrap-0"

	_, err := ReconcileVM(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 0, fc.CreateCalls)
	require.NotNil(t, s.HyperVMachine.Status.Adopted)
	assert.True(t, *s.HyperVMachine.Status.Adopted)
}

func TestReconcileVMStartsAdoptedButStoppedVM(t *testing.T) {
	fc := fake.New()
	fc.VMs["node0"] = &hyperv.VM{Name: "node0", ID: "g", State: hyperv.StateOff, Generation: 2}

	s := newScope(t, fc)
	_, err := ReconcileVM(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, 0, fc.CreateCalls, "adopted, not created")
	assert.Equal(t, 1, fc.StartCalls, "a stopped adopted VM must be started")
}

func TestReconcileVMCreateError(t *testing.T) {
	fc := fake.New()
	fc.CreateErr = assertErr{}
	s := newScope(t, fc)
	_, err := ReconcileVM(context.Background(), s)
	require.Error(t, err)
}

func TestDeleteVMStopThenRemove(t *testing.T) {
	fc := fake.New()
	fc.VMs["node0"] = &hyperv.VM{Name: "node0", ID: "g", State: hyperv.StateRunning, Generation: 2}
	s := newScope(t, fc)

	// Pass 1: running -> stop, not done.
	done, err := DeleteVM(context.Background(), s)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, 1, fc.StopCalls)
	assert.Equal(t, 0, fc.RemoveCalls)
	assert.Equal(t, infrav1.VirtualMachineStateDeleting, s.HyperVMachine.Status.VMState)

	// Pass 2: stopped -> remove, not done.
	done, err = DeleteVM(context.Background(), s)
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, 1, fc.RemoveCalls)

	// Pass 3: gone -> done.
	done, err = DeleteVM(context.Background(), s)
	require.NoError(t, err)
	assert.True(t, done)
}

func TestDeleteVMAlreadyGone(t *testing.T) {
	s := newScope(t, fake.New())
	done, err := DeleteVM(context.Background(), s)
	require.NoError(t, err)
	assert.True(t, done, "absent VM is immediately done")
}

func TestReconcileThenDeleteFullCycle(t *testing.T) {
	fc := fake.New()
	s := newScope(t, fc)
	fc.Addresses["node0"] = []string{"192.168.1.50"}

	_, err := ReconcileVM(context.Background(), s)
	require.NoError(t, err)
	assert.Equal(t, infrav1.VirtualMachineStateReady, s.HyperVMachine.Status.VMState)

	// Delete it: stop, remove, gone.
	var done bool
	for i := 0; i < 5 && !done; i++ {
		done, err = DeleteVM(context.Background(), s)
		require.NoError(t, err)
	}
	assert.True(t, done)
	_, exists := fc.VMs["node0"]
	assert.False(t, exists)
}

// assertErr is a trivial error used for error-injection tests.
type assertErr struct{}

func (assertErr) Error() string { return "injected failure" }
