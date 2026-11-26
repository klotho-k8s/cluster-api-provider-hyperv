/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/klotho-k8s/cluster-api-provider-hyperv/api/v1alpha1"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
	hvfake "github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv/fake"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, clusterv1.AddToScheme(s))
	require.NoError(t, infrav1.AddToScheme(s))
	return s
}

func TestClusterReconcileMissingObject(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	r := &HyperVClusterReconciler{Client: c, Scheme: c.Scheme()}
	res, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: "default", Name: "gone"},
	})
	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, res)
}

func TestClusterReconcileWaitsForEndpointThenProvisions(t *testing.T) {
	scheme := testScheme(t)
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl", Namespace: "default", UID: "cluster-uid"},
	}
	hvCluster := &infrav1.HyperVCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cl",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Cluster",
				Name:       "cl",
				UID:        "cluster-uid",
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, hvCluster).
		WithStatusSubresource(hvCluster).
		Build()
	r := &HyperVClusterReconciler{Client: c, Scheme: scheme}

	key := types.NamespacedName{Namespace: "default", Name: "cl"}

	// No endpoint yet: should requeue and not be provisioned.
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter)

	got := &infrav1.HyperVCluster{}
	require.NoError(t, c.Get(context.Background(), key, got))
	assert.Nil(t, got.Status.Initialization.Provisioned)
	assert.True(t, controllerContainsFinalizer(got.Finalizers, infrav1.ClusterFinalizer))

	// Provide an endpoint and reconcile again: now provisioned.
	got.Spec.ControlPlaneEndpoint = infrav1.APIEndpoint{Host: "10.0.0.2", Port: 6443}
	require.NoError(t, c.Update(context.Background(), got))

	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	require.NoError(t, err)

	require.NoError(t, c.Get(context.Background(), key, got))
	require.NotNil(t, got.Status.Initialization.Provisioned)
	assert.True(t, *got.Status.Initialization.Provisioned)
}

// machineGraph builds a Cluster/Machine/HyperVCluster/HyperVMachine/secret set
// wired up the way Cluster API would, so the machine reconciler can run against
// the controller-runtime fake client.
func machineGraph(t *testing.T, adoptExisting bool) (client.Client, *hvfake.Client, types.NamespacedName) {
	t.Helper()
	scheme := testScheme(t)

	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl", Namespace: "default", UID: "cluster-uid"},
		Spec: clusterv1.ClusterSpec{
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				Kind: infrav1.HyperVClusterKind,
				Name: "cl",
			},
		},
		Status: clusterv1.ClusterStatus{
			Initialization: clusterv1.ClusterInitializationStatus{
				InfrastructureProvisioned: ptr.To(true),
			},
		},
	}
	hvCluster := &infrav1.HyperVCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "cl", Namespace: "default"},
		Spec:       infrav1.HyperVClusterSpec{ControlPlaneEndpoint: infrav1.APIEndpoint{Host: "10.0.0.2", Port: 6443}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "node0-bootstrap", Namespace: "default"},
		Data:       map[string][]byte{"value": []byte("#cloud-config\n")},
	}
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node0",
			Namespace: "default",
			UID:       "machine-uid",
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "cl"},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: "cl",
			Bootstrap:   clusterv1.Bootstrap{DataSecretName: ptr.To("node0-bootstrap")},
		},
	}
	hvMachine := &infrav1.HyperVMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "node0",
			Namespace: "default",
			Labels:    map[string]string{clusterv1.ClusterNameLabel: "cl"},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: clusterv1.GroupVersion.String(),
				Kind:       "Machine",
				Name:       "node0",
				UID:        "machine-uid",
			}},
		},
		Spec: infrav1.HyperVMachineSpec{
			Image:      `C:\base\ubuntu.vhdx`,
			SwitchName: "External",
			Generation: 2,
			CPUCount:   2,
			MemoryMiB:  2048,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster, hvCluster, secret, machine, hvMachine).
		WithStatusSubresource(hvCluster, hvMachine).
		Build()

	fc := hvfake.New()
	if adoptExisting {
		fc.VMs["node0"] = &hyperv.VM{Name: "node0", ID: "preexisting-guid", State: hyperv.StateRunning, Generation: 2}
		fc.Addresses["node0"] = []string{"10.0.0.50"}
	}
	return c, fc, types.NamespacedName{Namespace: "default", Name: "node0"}
}

func TestMachineReconcileAdoptsExistingVM(t *testing.T) {
	c, fc, key := machineGraph(t, true)
	r := &HyperVMachineReconciler{Client: c, Scheme: c.Scheme(), HyperVClient: fc}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	require.NoError(t, err)

	got := &infrav1.HyperVMachine{}
	require.NoError(t, c.Get(context.Background(), key, got))
	assert.Equal(t, 0, fc.CreateCalls, "must adopt the pre-existing VM, not create one")
	assert.Equal(t, "hyperv://preexisting-guid", got.Spec.ProviderID)
	require.NotNil(t, got.Status.Adopted)
	assert.True(t, *got.Status.Adopted)
	require.NotNil(t, got.Status.Initialization.Provisioned)
	assert.True(t, *got.Status.Initialization.Provisioned)
	assert.True(t, controllerContainsFinalizer(got.Finalizers, infrav1.MachineFinalizer))
}

func TestMachineReconcileCreatesWhenAbsent(t *testing.T) {
	c, fc, key := machineGraph(t, false)
	r := &HyperVMachineReconciler{Client: c, Scheme: c.Scheme(), HyperVClient: fc}

	// First reconcile: creates and starts the VM, but no guest address yet.
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Equal(t, 1, fc.CreateCalls)
	assert.Equal(t, 1, fc.AttachCloudInitCfg)

	// Guest reports an address; next reconcile marks it provisioned.
	fc.Addresses["node0"] = []string{"10.0.0.51"}
	_, err = r.Reconcile(context.Background(), reconcile.Request{NamespacedName: key})
	require.NoError(t, err)

	got := &infrav1.HyperVMachine{}
	require.NoError(t, c.Get(context.Background(), key, got))
	assert.Equal(t, 1, fc.CreateCalls, "must not create a second VM")
	require.NotNil(t, got.Status.Adopted)
	assert.False(t, *got.Status.Adopted)
	require.NotNil(t, got.Status.Initialization.Provisioned)
	assert.True(t, *got.Status.Initialization.Provisioned)
}

func controllerContainsFinalizer(finalizers []string, want string) bool {
	for _, f := range finalizers {
		if f == want {
			return true
		}
	}
	return false
}
