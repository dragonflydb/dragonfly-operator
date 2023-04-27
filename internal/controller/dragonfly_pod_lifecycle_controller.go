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
	"time"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type DfPodLifeCycleReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	EventRecorder record.EventRecorder
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

	// check for pod readiness
	if pod.Status.PodIP == "" || pod.Status.Phase != corev1.PodRunning || !pod.Status.ContainerStatuses[0].Ready {
		log.Info("Pod is not ready yet")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	dfi, err := GetDragonflyInstanceFromPod(ctx, r.Client, &pod, log)
	if err != nil {
		log.Info("Pod does not belong to a Dragonfly instance")
		return ctrl.Result{}, nil
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
	role, ok := pod.Labels[resources.Role]
	// New pod with No resources.Role
	if !ok {
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
					log.Error(err, "could not mark replica from db")
					return ctrl.Result{RequeueAfter: 5 * time.Second}, err
				}

				r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Configured a new replica")
			}
		}
	} else if pod.DeletionTimestamp != nil {
		// pod deletion event
		// configure replication if its a master pod
		// do nothing if its a replica pod
		log.Info("Pod is being deleted", "pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
		// Check if there is an active master
		if pod.Labels[resources.Role] == resources.Master {
			log.Info("master is being removed. re-configuring replication")
			if err := dfi.configureReplication(ctx); err != nil {
				log.Error(err, "couldn't find healthy and mark active")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}
			r.EventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Updated master instance")
		} else if pod.Labels[resources.Role] == resources.Replica {
			log.Info("replica is being deleted. nothing to do")
		}
	} else {
		// is something wrong? check if all pods have a matching role and revamp accordingly
		log.Info("Non-deletion event for a pod with an existing role. checking if something is wrong", "pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name), "role", role)

		if err := dfi.checkAndConfigureReplication(ctx); err != nil {
			log.Error(err, "could not check and configure replication")
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
					if e.ObjectNew.GetLabels()[resources.KubernetesAppNameLabelKey] == "dragonfly" {
						return true
					}

					return false
				},
				CreateFunc: func(e event.CreateEvent) bool {
					if e.Object.GetLabels()[resources.KubernetesAppNameLabelKey] == "dragonfly" {
						return true
					}
					return false
				},
				DeleteFunc: func(e event.DeleteEvent) bool {
					if e.Object.GetLabels()[resources.KubernetesAppNameLabelKey] == "dragonfly" {
						return true
					}
					return false
				},
			}).
		For(&corev1.Pod{}).
		Complete(r)
}
