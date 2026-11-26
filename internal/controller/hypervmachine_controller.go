/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

package controller

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/klotho-k8s/cluster-api-provider-hyperv/api/v1alpha1"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/internal/service/vmservice"
	"github.com/klotho-k8s/cluster-api-provider-hyperv/pkg/hyperv"
)

// HyperVMachineReconciler reconciles a HyperVMachine object.
type HyperVMachineReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// HyperVClient is the Hyper-V backend shared across reconciles. In agent mode
	// it is a gRPC client to the host agent; in local mode a PowerShell client.
	// The backend builds the cloud-init ISO on the host, so the controller needs
	// no host filesystem access.
	HyperVClient hyperv.Client
}

// SetupWithManager sets up the controller with the Manager.
func (r *HyperVMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.HyperVMachine{}).
		Watches(
			&clusterv1.Machine{},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind(infrav1.HyperVMachineKind))),
		).
		Complete(r)
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=hypervmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=hypervmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=hypervmachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=hypervmachinetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch

// Reconcile moves the current state of a HyperVMachine closer to its desired state.
func (r *HyperVMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)

	hyperVMachine := &infrav1.HyperVMachine{}
	if err := r.Get(ctx, req.NamespacedName, hyperVMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	machine, err := util.GetOwnerMachine(ctx, r.Client, hyperVMachine.ObjectMeta)
	if err != nil {
		// The owner Machine OwnerRef is set but the Machine is gone. Without this
		// branch a being-deleted HyperVMachine would error-loop forever and never
		// drop its finalizer, blocking cluster deletion. Mirror the CAPD/CAPMOX
		// unblock behavior.
		if apierrors.IsNotFound(err) {
			if !hyperVMachine.DeletionTimestamp.IsZero() {
				logger.Info("Owner Machine not found while HyperVMachine is being deleted; removing finalizer to unblock cluster delete. The underlying Hyper-V VM may still exist and should be verified by an administrator.")
				if ctrlutil.RemoveFinalizer(hyperVMachine, infrav1.MachineFinalizer) {
					if err := r.Update(ctx, hyperVMachine); err != nil {
						return ctrl.Result{}, errors.Wrap(err, "removing finalizer after owner Machine not found")
					}
				}
				return ctrl.Result{}, nil
			}
			logger.Info("Owner Machine not found and HyperVMachine is not being deleted; skipping reconcile.")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if machine == nil {
		logger.Info("Machine Controller has not yet set OwnerRef")
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("machine", klog.KObj(machine))

	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		logger.Info("Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, nil
	}

	if annotations.IsPaused(cluster, hyperVMachine) {
		logger.Info("HyperVMachine or linked Cluster is marked as paused, not reconciling")
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("cluster", klog.KObj(cluster))

	hyperVCluster := &infrav1.HyperVCluster{}
	infraClusterKey := client.ObjectKey{
		Namespace: hyperVMachine.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}
	if err := r.Client.Get(ctx, infraClusterKey, hyperVCluster); err != nil {
		logger.Info("HyperVCluster is not available yet")
		return ctrl.Result{}, nil
	}

	patchHelper, err := patch.NewHelper(hyperVMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to init patch helper")
	}
	defer func() {
		if err := patchHelper.Patch(ctx, hyperVMachine); err != nil && reterr == nil {
			reterr = err
		}
	}()

	scope := &vmservice.Scope{
		Client:        r.HyperVClient,
		Cluster:       cluster,
		HyperVCluster: hyperVCluster,
		Machine:       machine,
		HyperVMachine: hyperVMachine,
		Logger:        logger,
	}

	if !hyperVMachine.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, scope)
	}

	return r.reconcileNormal(ctx, scope, patchHelper)
}

func (r *HyperVMachineReconciler) reconcileNormal(ctx context.Context, scope *vmservice.Scope, patchHelper *patch.Helper) (ctrl.Result, error) {
	hyperVMachine := scope.HyperVMachine

	if !ptr.Deref(scope.Cluster.Status.Initialization.InfrastructureProvisioned, false) {
		scope.Logger.Info("Cluster infrastructure is not ready yet")
		conditions.Set(hyperVMachine, metav1.Condition{
			Type:   infrav1.HyperVMachineVirtualMachineProvisionedCondition,
			Status: metav1.ConditionFalse,
			Reason: infrav1.HyperVMachineVirtualMachineProvisionedWaitingForClusterInfrastructureReason,
		})
		return ctrl.Result{}, nil
	}

	if scope.Machine.Spec.Bootstrap.DataSecretName == nil {
		scope.Logger.Info("Bootstrap data secret reference is not yet available")
		conditions.Set(hyperVMachine, metav1.Condition{
			Type:   infrav1.HyperVMachineVirtualMachineProvisionedCondition,
			Status: metav1.ConditionFalse,
			Reason: infrav1.HyperVMachineVirtualMachineProvisionedWaitingForBootstrapDataReason,
		})
		return ctrl.Result{}, nil
	}

	// Register the finalizer before any VM is created so a delete can never
	// orphan a Hyper-V VM. Persist it immediately: the deferred status patch is
	// not enough, because a crash between VM creation and that patch would lose
	// the finalizer and leak the VM.
	if ctrlutil.AddFinalizer(hyperVMachine, infrav1.MachineFinalizer) {
		if err := patchHelper.Patch(ctx, hyperVMachine); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to persist finalizer")
		}
	}

	bootstrapData, err := r.getBootstrapData(ctx, scope)
	if err != nil {
		return ctrl.Result{}, err
	}
	scope.BootstrapData = bootstrapData

	vm, err := vmservice.ReconcileVM(ctx, scope)
	if err != nil {
		scope.Logger.Error(err, "error reconciling VM")
		conditions.Set(hyperVMachine, metav1.Condition{
			Type:    infrav1.HyperVMachineVirtualMachineProvisionedCondition,
			Status:  metav1.ConditionFalse,
			Reason:  infrav1.HyperVMachineVirtualMachineProvisionedProvisionFailedReason,
			Message: err.Error(),
		})
		return ctrl.Result{}, errors.Wrap(err, "failed to reconcile VM")
	}

	if hyperVMachine.Status.VMState != infrav1.VirtualMachineStateReady {
		scope.Logger.Info("VM is not ready yet", "state", hyperVMachine.Status.VMState, "vmState", vm.State)
		conditions.Set(hyperVMachine, metav1.Condition{
			Type:   infrav1.HyperVMachineVirtualMachineProvisionedCondition,
			Status: metav1.ConditionFalse,
			Reason: infrav1.HyperVMachineVirtualMachineProvisionedWaitingForAddressesReason,
		})
		return ctrl.Result{RequeueAfter: infrav1.DefaultReconcilerRequeue}, nil
	}

	hyperVMachine.Status.Initialization.Provisioned = ptr.To(true)
	conditions.Set(hyperVMachine, metav1.Condition{
		Type:   infrav1.HyperVMachineVirtualMachineProvisionedCondition,
		Status: metav1.ConditionTrue,
		Reason: "Provisioned",
	})
	scope.Logger.Info("HyperVMachine is ready", "providerID", hyperVMachine.Spec.ProviderID)

	return ctrl.Result{}, nil
}

func (r *HyperVMachineReconciler) reconcileDelete(ctx context.Context, scope *vmservice.Scope) (ctrl.Result, error) {
	scope.Logger.Info("Handling deleted HyperVMachine")

	conditions.Set(scope.HyperVMachine, metav1.Condition{
		Type:   infrav1.HyperVMachineVirtualMachineProvisionedCondition,
		Status: metav1.ConditionFalse,
		Reason: infrav1.HyperVMachineVirtualMachineProvisionedDeletingReason,
	})

	done, err := vmservice.DeleteVM(ctx, scope)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !done {
		// A VM state transition happened (stop or remove); requeue to continue.
		return ctrl.Result{RequeueAfter: infrav1.DefaultReconcilerRequeue}, nil
	}

	ctrlutil.RemoveFinalizer(scope.HyperVMachine, infrav1.MachineFinalizer)
	return ctrl.Result{}, nil
}

// getBootstrapData returns the decoded bootstrap secret for the machine.
func (r *HyperVMachineReconciler) getBootstrapData(ctx context.Context, scope *vmservice.Scope) ([]byte, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{
		Namespace: scope.HyperVMachine.Namespace,
		Name:      *scope.Machine.Spec.Bootstrap.DataSecretName,
	}
	if err := r.Client.Get(ctx, key, secret); err != nil {
		return nil, errors.Wrapf(err, "failed to retrieve bootstrap data secret %s", key)
	}
	value, ok := secret.Data["value"]
	if !ok {
		return nil, fmt.Errorf("bootstrap data secret %s is missing the \"value\" key", key)
	}
	return value, nil
}
