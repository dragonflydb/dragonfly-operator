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

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type DfPodLifeCycleReconciler struct {
	Reconciler
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

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

			master = selectMasterCandidate(allPods.Items, dfi)
			if master == nil {
				log.Info("no healthy pod available to set up a master")
				return ctrl.Result{}, nil
			}

			if err = dfi.configureReplication(ctx, master); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to configure replication: %w", err)
			}
			// re-evaluate readiness after replication changes.
			podReady, readinessErr = dfi.isPodReady(ctx, &pod)
			if readinessErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to verify pod readiness: %w", readinessErr)
			}
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to get master pod: %w", err)
		}
	}

	role, err := dfi.getRedisRole(ctx, master)
	if err != nil {
		log.Error(err, "failed to get redis role for labeled master", "pod", master.Name)
	} else if role == resources.Replica {
		log.Info("Pod labeled as master is running as replica. Promoting it.", "pod", master.Name)
		if err := dfi.replicaOfNoOne(ctx, master); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to promote master: %w", err)
		}
	}

	if !podReady {
		return ctrl.Result{}, nil
	}

	if roleExists(&pod) {
		if dfi.getStatus().Phase != PhaseReady && dfi.getStatus().Phase != PhaseReadyOld && dfi.getStatus().Phase != PhaseConfiguring {
			return ctrl.Result{}, nil
		}

		// is something wrong? check if all replicas have a matching role and revamp accordingly
		log.Info("non-deletion event for a pod with an existing role. checking if something is wrong", "pod", pod.Name, "role", pod.Labels[resources.RoleLabelKey])

		if allConfigured, err := dfi.checkAndConfigureReplicas(ctx, master.Status.PodIP); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to check and configure replicas: %w", err)
		} else if !allConfigured {
			log.Info("not all replicas are ready, requeueing")
			return ctrl.Result{Requeue: true}, nil
		} else if dfi.getStatus().Phase == PhaseConfiguring {
			status := dfi.getStatus()
			status.Phase = PhaseReady
			if err = dfi.patchStatus(ctx, status); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update the dragonfly status: %w", err)
			}
		}

		r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Checked and configured replication")
	} else {
		log.Info("pod does not have a role label", "pod", pod.Name)

		if err = dfi.configureReplica(ctx, &pod, master.Status.PodIP); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to configure pod as replica: %w", err)
		}

		r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Configured a new replica")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DfPodLifeCycleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithEventFilter(
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
			}).
		Named("DragonflyPodLifecycle").
		For(&corev1.Pod{}).
		Complete(r)
}
