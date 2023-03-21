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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type HealthReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Configurer replicationMarker
}

//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// This reconcile events focuses on configuring the given pods either as a `master`
// or `replica` as they go through their lifecycle. This also focus on the failing
// over to replica's part to make sure one `master` is always available.
func (r *HealthReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.Info("Received", "pod", req.NamespacedName)
	var pod corev1.Pod
	err := r.Client.Get(ctx, req.NamespacedName, &pod, &client.GetOptions{})
	if err != nil {
		// TODO: Handle Pod Deletion in a different way
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Pod is not ready to be processed yet
	if pod.Status.PodIP == "" {
		log.Info("Pod IP is not yet available")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	df, err := GetDragonflyInstanceFromPod(ctx, r.Client, &pod, r.Configurer, log)
	if err != nil {
		log.Info("Pod does not belong to a Dragonfly instance")
		return ctrl.Result{}, nil
	}

	if df.df.Status.Phase == "" {
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
		log.Info("Replication is not configured yet")
		if df.df.Status.Phase == PhaseResoucesCreated {
			// Make it ready
			log.Info("Dragonfly object is only initialized. Configuring replication for the first time")

			if err = df.initReplication(ctx); err != nil {
				log.Error(err, "could not initialize replication")
				return ctrl.Result{}, err
			}

		} else if df.df.Status.Phase == PhaseReady {
			// Probably a pod restart do the needful
			log.Info("Pod restart from a ready Dragonfly instance")

			// What should this pod be?
			// If there is no active master, find and mark an healthy instance
			// if there is an active master, mark it as a replica
			// Check if there is an active master
			exists, err := df.masterExists(ctx)
			if err != nil {
				log.Error(err, "could not check if active master exists")
				return ctrl.Result{}, err
			}

			if !exists {
				log.Info("Master does not exist. Configuring Replication")
				if err := df.initReplication(ctx); err != nil {
					log.Error(err, "couldn't find healthy and mark active")
					return ctrl.Result{}, err
				}

			} else {
				log.Info(fmt.Sprintf("Master exists. Configuring %s as replica", pod.Status.PodIP))
				if err := df.configureReplica(ctx, &pod); err != nil {
					log.Error(err, "could not mark replica from db")
					return ctrl.Result{}, err
				}
			}
		}

	} else {
		log.Info("Role exists already", "pod", fmt.Sprintf("%s/%s", pod.Namespace, pod.Name), "role", role)
		// TODO: What do you do here?
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *HealthReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
