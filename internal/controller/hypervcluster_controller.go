/*
Copyright 2026 Luther Monson.

Licensed under the MIT License. See LICENSE in the project root for details.
*/

// Package controller implements the HyperVCluster and HyperVMachine reconcilers.
package controller

import (
	"context"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	clusterutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlutil "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/klotho-k8s/cluster-api-provider-hyperv/api/v1alpha1"
)

// HyperVClusterReconciler reconciles a HyperVCluster object.
type HyperVClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// SetupWithManager sets up the controller with the Manager.
func (r *HyperVClusterReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.HyperVCluster{}).
		WithEventFilter(predicates.ResourceNotPaused(r.Scheme, ctrl.LoggerFrom(ctx))).
		Watches(&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(clusterutil.ClusterToInfrastructureMapFunc(ctx, infrav1.GroupVersion.WithKind(infrav1.HyperVClusterKind), mgr.GetClient(), &infrav1.HyperVCluster{})),
			builder.WithPredicates(predicates.ClusterUnpaused(r.Scheme, ctrl.LoggerFrom(ctx)))).
		Complete(r)
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=hypervclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=hypervclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=hypervclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=hypervclustertemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch

// Reconcile moves the current state of a HyperVCluster closer to its desired state.
func (r *HyperVClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)

	hyperVCluster := &infrav1.HyperVCluster{}
	if err := r.Client.Get(ctx, req.NamespacedName, hyperVCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	cluster, err := clusterutil.GetOwnerCluster(ctx, r.Client, hyperVCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		logger.Info("Waiting for Cluster Controller to set OwnerRef on HyperVCluster")
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("cluster", klog.KObj(cluster))

	if annotations.IsPaused(cluster, hyperVCluster) {
		logger.Info("HyperVCluster or owning Cluster is marked as paused, not reconciling")
		return ctrl.Result{}, nil
	}

	patchHelper, err := patch.NewHelper(hyperVCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to init patch helper")
	}
	defer func() {
		if err := patchHelper.Patch(ctx, hyperVCluster); err != nil && reterr == nil {
			reterr = err
		}
	}()

	if !hyperVCluster.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, hyperVCluster)
	}

	return r.reconcileNormal(ctx, hyperVCluster)
}

func (r *HyperVClusterReconciler) reconcileNormal(_ context.Context, hyperVCluster *infrav1.HyperVCluster) (reconcile.Result, error) {
	ctrlutil.AddFinalizer(hyperVCluster, infrav1.ClusterFinalizer)

	// Surface the configured failure domains for the Cluster API topology.
	hyperVCluster.Status.FailureDomains = buildFailureDomains(hyperVCluster.Spec.FailureDomains)

	// Hyper-V has no load balancer: the control plane endpoint is user-provided
	// (a static VIP, e.g. kube-vip) or the first control plane address injected
	// by whoever provisions it. We only validate that it is present.
	if hyperVCluster.Spec.ControlPlaneEndpoint.IsZero() {
		conditions.Set(hyperVCluster, metav1.Condition{
			Type:    infrav1.HyperVClusterHyperVAvailableCondition,
			Status:  metav1.ConditionFalse,
			Reason:  infrav1.HyperVClusterHyperVAvailableMissingControlPlaneEndpointReason,
			Message: "The HyperVCluster is waiting for a ControlPlaneEndpoint (static VIP or first control plane address)",
		})
		return ctrl.Result{RequeueAfter: infrav1.DefaultReconcilerRequeue}, nil
	}

	conditions.Set(hyperVCluster, metav1.Condition{
		Type:   infrav1.HyperVClusterHyperVAvailableCondition,
		Status: metav1.ConditionTrue,
		Reason: "Available",
	})
	hyperVCluster.Status.Initialization.Provisioned = ptr.To(true)

	return ctrl.Result{}, nil
}

func (r *HyperVClusterReconciler) reconcileDelete(_ context.Context, hyperVCluster *infrav1.HyperVCluster) (reconcile.Result, error) {
	// HyperVMachines carry their own finalizers and are reaped independently; the
	// cluster holds no host-side resources of its own, so it just releases its
	// finalizer.
	conditions.Set(hyperVCluster, metav1.Condition{
		Type:   infrav1.HyperVClusterHyperVAvailableCondition,
		Status: metav1.ConditionFalse,
		Reason: infrav1.HyperVClusterHyperVAvailableDeletingReason,
	})
	ctrlutil.RemoveFinalizer(hyperVCluster, infrav1.ClusterFinalizer)
	return ctrl.Result{}, nil
}

// buildFailureDomains maps the spec's failure domain names into the Cluster API
// status representation, marking every domain usable by control plane machines.
func buildFailureDomains(names []string) []clusterv1.FailureDomain {
	if len(names) == 0 {
		return nil
	}
	out := make([]clusterv1.FailureDomain, 0, len(names))
	for _, name := range names {
		out = append(out, clusterv1.FailureDomain{
			Name:         name,
			ControlPlane: ptr.To(true),
		})
	}
	return out
}
