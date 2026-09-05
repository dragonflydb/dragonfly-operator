/*
Copyright 2023 DragonflyDB authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type DfPodLifeCycleReconciler struct {
	Reconciler
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// This reconcile events focuses on configuring the given pods either as a `master`
// or `replica` as they go through their lifecycle. This also focus on the failing
// over to replica's part to make sure one `master` is always available.
func (r *DfPodLifeCycleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.Info("received", "pod", req.NamespacedName)
	var pod corev1.Pod
	err := r.Client.Get(ctx, req.NamespacedName, &pod)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("failed to get pod: %w", err))
	}

	dfName, err := getDragonflyName(&pod)
	if err != nil {
		log.Error(err, "failed to get Dragonfly name from pod labels")
		return ctrl.Result{}, nil
	}

	dfi, err := r.getDragonflyInstance(ctx, types.NamespacedName{
		Name:      dfName,
		Namespace: pod.Namespace,
	}, log)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("failed to get dragonfly instance: %w", err))
	}
	defer dfi.Close()

	// Returning early only defers this pod's own reconcile; master election is
	// driven by every pod's events, not just this one.
	if result, err := dfi.reconcileClientDisconnect(ctx, &pod); !result.IsZero() || err != nil {
		return result, err
	}

	podReady, readinessErr := dfi.isPodReady(ctx, &pod)
	if readinessErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to verify pod readiness: %w", readinessErr)
	}

	master, err := dfi.getMaster(ctx)
	if err != nil {
		if isMasterError(err) {
			log.Info("failed to get master pod", "error", err)

			if errors.Is(err, ErrIncorrectMasters) || errors.Is(err, ErrNoHealthyMaster) {
				if err = dfi.deleteMasterRoleLabel(ctx); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to delete master role label: %w", err)
				}
			}

			allPods, err := dfi.getPods(ctx)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to list dragonfly pods: %w", err)
			}

			masterCandidate := selectMasterCandidate(allPods.Items, func(p *corev1.Pod) bool {
				ready, err := dfi.isPodReady(ctx, p)
				if err != nil {
					log.Error(err, "failed to check readiness for candidate", "pod", p.Name)
					return false
				}
				return ready
			}, func(p *corev1.Pod) MasterCandidate {
				return dfi.getMasterCandidate(ctx, p)
			})
			if masterCandidate == nil {
				log.Info("no healthy pod available to set up a master")
				// Always requeue when no master candidate is available to avoid
				// stalling master election on transient readiness errors.
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			master = masterCandidate

			if err = dfi.configureReplication(ctx, master); err != nil {
				// Check for transient replication errors.
				if isReplicationCancelledError(err) {
					log.Info("replication cancelled during initial setup (transient), will retry", "error", err)
					return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
				}
				return ctrl.Result{}, fmt.Errorf("failed to configure replication: %w", err)
			}
			// Replication was just configured. Return and let the next reconciliation
			// work with fresh pod data (the pod now has updated labels).
			r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Initial replication configured")

			// Ensure we still poll the current pod if it triggered this event and isn't ready
			if !podReady {
				log.Info("pod not ready yet, will retry after initial replication config", "pod", pod.Name)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			return ctrl.Result{}, nil
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to get master pod: %w", err)
		}
	}

	role, err := dfi.getRedisRole(ctx, master)
	if err != nil {
		log.Info("failed to verify master status in redis (ignoring)", "error", err)
	} else if role != resources.Master {
		log.Info("Pod labeled as master is running as replica. Promoting it.", "pod", master.Name)
		if err := dfi.replicaOfNoOne(ctx, master); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to promote master: %w", err)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	if !podReady {
		log.Info("pod not ready yet, will retry", "pod", pod.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if roleExists(&pod) {
		if dfi.getStatus().Phase == PhaseReady || dfi.getStatus().Phase == PhaseReadyOld {
			// is something wrong? check if all replicas have a matching role and revamp accordingly
			log.Info("non-deletion event for a pod with an existing role. checking if something is wrong", "pod", pod.Name, "role", pod.Labels[resources.RoleLabelKey])

			if err = dfi.checkAndConfigureReplicas(ctx, master.Status.PodIP); err != nil {
				// Check for transient replication errors
				if isReplicationCancelledError(err) {
					log.Info("replication cancelled (transient), will retry", "error", err)
					return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
				}
				return ctrl.Result{}, fmt.Errorf("failed to check and configure replicas: %w", err)
			}

			r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Checked and configured replication")
		}
	} else {
		log.Info("pod does not have a role label", "pod", pod.Name)

		if err = dfi.configureReplica(ctx, &pod, master.Status.PodIP); err != nil {
			// Check for transient replication errors
			if isReplicationCancelledError(err) {
				log.Info("replication cancelled (transient), will retry", "error", err)
				return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
			}
			return ctrl.Result{}, fmt.Errorf("failed to configure pod as replica: %w", err)
		}

		r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Configured a new replica")
	}

	if dfi.df.Spec.EnableReplicationReadinessGate {
		if isMaster(&pod) {
			if err := dfi.patchReplicationReadyCondition(ctx, &pod, true); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to patch replication ready condition: %w", err)
			}
		} else if isReplica(&pod) {
			stable, stableErr := dfi.isReplicaStable(ctx, &pod)
			if stableErr != nil {
				stable = false
			}
			if err := dfi.patchReplicationReadyCondition(ctx, &pod, stable); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to patch replication ready condition: %w", err)
			}
			if !stable {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}
	}

	return ctrl.Result{}, nil
}

// pendingClientDisconnectIndex keeps the endpoint slice handler off a full pod scan.
const pendingClientDisconnectIndex = "pendingClientDisconnect"

func indexPendingClientDisconnect(obj client.Object) []string {
	if _, pending := pendingClientDisconnectSince(obj.(*corev1.Pod)); !pending {
		return nil
	}

	return []string{"true"}
}

// SetupWithManager sets up the controller with the Manager.
func (r *DfPodLifeCycleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &corev1.Pod{},
		pendingClientDisconnectIndex, indexPendingClientDisconnect); err != nil {
		return fmt.Errorf("failed to index pods pending a client disconnect: %w", err)
	}

	// This predicate reads pod labels, so it must stay scoped to the pod source
	// rather than going back to WithEventFilter, which also covers the slices below.
	return ctrl.NewControllerManagedBy(mgr).
		Named("DragonflyPodLifecycle").
		For(&corev1.Pod{}, builder.WithPredicates(
			predicate.Funcs{
				UpdateFunc: func(e event.UpdateEvent) bool {
					return e.ObjectNew.GetLabels()[resources.KubernetesAppNameLabelKey] == resources.KubernetesAppName
				},
				CreateFunc: func(e event.CreateEvent) bool {
					return e.Object.GetLabels()[resources.KubernetesAppNameLabelKey] == resources.KubernetesAppName
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					return e.Object.GetLabels()[resources.KubernetesAppNameLabelKey] == resources.KubernetesAppName
				},
			})).
		Watches(&discoveryv1.EndpointSlice{}, handler.EnqueueRequestsFromMapFunc(r.podsAwaitingClientDisconnect)).
		Complete(r)
}

// podsAwaitingClientDisconnect enqueues the demoted pods of the namespace whose endpoints changed.
func (r *DfPodLifeCycleReconciler) podsAwaitingClientDisconnect(ctx context.Context, endpointSlice client.Object) []reconcile.Request {
	if _, ok := endpointSlice.GetLabels()[discoveryv1.LabelServiceName]; !ok {
		return nil
	}

	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods,
		client.InNamespace(endpointSlice.GetNamespace()),
		client.MatchingFields{pendingClientDisconnectIndex: "true"},
	); err != nil {
		log.FromContext(ctx).Error(err, "failed to list the pods pending a client disconnect")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(pods.Items))
	for i := range pods.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&pods.Items[i])})
	}

	return requests
}
