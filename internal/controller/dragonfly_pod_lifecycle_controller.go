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
	"fmt"
	"k8s.io/apimachinery/pkg/types"
	"time"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	corev1 "k8s.io/api/core/v1"
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

	log.Info("Received", "pod", req.NamespacedName)
	var pod corev1.Pod
	err := r.Client.Get(ctx, req.NamespacedName, &pod, &client.GetOptions{})
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	dfName, ok := pod.Labels[resources.DragonflyNameLabelKey]
	if !ok {
		log.Info("failed to get Dragonfly name from pod labels")
		return ctrl.Result{}, nil
	}

	dfi, err := r.getDragonflyInstance(ctx, types.NamespacedName{
		Name:      dfName,
		Namespace: pod.Namespace,
	}, log)
	if err != nil {
		log.Info("Pod does not belong to a Dragonfly instance")
		return ctrl.Result{}, nil
	}

	// Get the role of the pod
	role, roleExists := pod.Labels[resources.Role]
	if !isPodReady(pod) {
		if roleExists && role == resources.Master {
			log.Info("Master pod is not ready, initiating failover", "pod", req.NamespacedName)
			err := dfi.configureReplication(ctx)
			if err != nil {
				log.Error(err, "Failed to initiate failover")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}
			return ctrl.Result{}, nil
		} else {
			log.Info("Pod is not ready yet", "pod", req.NamespacedName)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	if dfi.df.Status.Phase == "" {
		// retry after resources are created
		// Phase should be initialized by the time this is called
		log.Info("Dragonfly object is not initialized yet")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Given a Pod Update, What do you do?
	// If it does not have a `resources.Role`,
	// - If the Dragonfly Object is in initialization phase, Do a init replication i.e set for first time.
	// - If the Dragonfly object status is Ready, Then this is a pod restart.
	// 	- If there is master already, add this as replica
	//	- If there is no master, find a healthy instance and mark it as master
	//
	// New pod with No resources.Role
	if !roleExists {
		log.Info("No replication role was set yet", "phase", dfi.df.Status.Phase)
		if dfi.df.Status.Phase == PhaseResourcesCreated {
			// Make it ready
			log.Info("Dragonfly object is only initialized. Configuring replication for the first time")
			if err = dfi.configureReplication(ctx); err != nil {
				log.Info("could not initialize replication. will retry", "error", err)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "configured replication for first time")
		} else if dfi.df.Status.Phase == PhaseReady {
			// Pod event either from a restart or a resource update (i.e less/more replicas)
			log.Info("Pod restart from a ready Dragonfly instance")

			// What should this pod be?
			// If there is no active master, find and mark an healthy instance
			// if there is an active master, mark it as a replica
			// Check if there is an active master
			exists, err := dfi.masterExists(ctx)
			if err != nil {
				log.Error(err, "could not check if active master exists")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}

			if !exists {
				log.Info("Master does not exist. Configuring Replication")
				if err := dfi.configureReplication(ctx); err != nil {
					log.Error(err, "couldn't find healthy and mark active")
					return ctrl.Result{RequeueAfter: 5 * time.Second}, err
				}

				r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Updated master instance")
			} else {
				log.Info(fmt.Sprintf("Master exists. Configuring %s as replica", pod.Status.PodIP))
				if err := dfi.configureReplica(ctx, &pod); err != nil {
					log.Error(err, "could not mark replica from db. retrying")
					return ctrl.Result{RequeueAfter: 5 * time.Second}, err
				}

				r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Configured a new replica")
			}
		}
	} else if isPodMarkedForDeletion(pod) {
		// pod deletion event
		// configure replication if its a master pod
		// do nothing if its a replica pod
		log.Info("Pod is being deleted", "pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
		// Check if there is an active master
		if pod.Labels[resources.Role] == resources.Master {
			log.Info("master is being removed")
			if dfi.df.Status.IsRollingUpdate {
				log.Info("rolling update in progress. nothing to do")
				return ctrl.Result{}, nil
			}

			log.Info("master is being removed. configuring replication")
			if err := dfi.configureReplication(ctx); err != nil {
				log.Error(err, "couldn't find healthy and mark active")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}
			r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Updated master instance")
		} else if pod.Labels[resources.Role] == resources.Replica {
			log.Info("replica is being deleted. nothing to do")
		}
	} else {
		if dfi.df.Status.IsRollingUpdate {
			log.Info("rolling update in progress. nothing to do")
			return ctrl.Result{}, nil
		}

		// is something wrong? check if all pods have a matching role and revamp accordingly
		log.Info("Non-deletion event for a pod with an existing role. checking if something is wrong", "pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name), "role", role)

		if err := dfi.checkAndConfigureReplication(ctx); err != nil {
			log.Error(err, "could not check and configure replication. retrying")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}

		r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Checked and configured replication")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DfPodLifeCycleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithEventFilter(
			predicate.Funcs{
				UpdateFunc: func(e event.UpdateEvent) bool {
					return e.ObjectNew.GetLabels()[resources.KubernetesAppNameLabelKey] == "dragonfly"
				},
				CreateFunc: func(e event.CreateEvent) bool {
					return e.Object.GetLabels()[resources.KubernetesAppNameLabelKey] == "dragonfly"
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					return e.Object.GetLabels()[resources.KubernetesAppNameLabelKey] == "dragonfly"
				},
			}).
		Named("DragonflyPodLifecycle").
		For(&corev1.Pod{}).
		Complete(r)
}
