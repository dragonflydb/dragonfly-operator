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

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type HealthReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// This reconcile events focuses on marking the given pods either as a `master`
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

	// Get the Dragonfly Object
	df, err := r.GetDFFromPod(ctx, &pod)
	if err != nil {
		log.Error(err, "could not get Dragonfly object")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if df.Status.Phase == "" {
		// retry after resources are created
		// Phase should be initialized by the time this is called
		log.Info("Dragonfly object is not initialized yet")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// Given a Pod Update, What do you do?
	// If it does not have a `resources.Role`, Check with the DragonFly Object Status and do a revamp on those
	// objects.
	// It it has a `resources.Role`, Check healthiness of the pod and act if its a master
	// Pod deletion or unhealthy
	role, ok := pod.Labels[resources.Role]
	// New pod with No resources.Role
	if !ok {
		log.Info("No role found on the pod")
		if df.Status.Phase == PhaseInitialized {
			// Make it ready
			log.Info("DragonFly object is only initialized. Configuring replication for the first time")
			if err = configureReplication(ctx, r.Client, df); err != nil {
				log.Error(err, "couldn't find and mark active during revamp")
				return ctrl.Result{}, err
			}

		} else if df.Status.Phase == PhaseReady {
			// Probably a pod restart do the needful
			log.Info("Pod restart from a ready Dragonfly instance")

			// What should this pod be?
			// If there is no active master, find and mark an healthy instance
			// if there is an active master, mark it as a replica
			// Check if there is an active master
			exists, err := activeMasterExists(ctx, r.Client, df)
			if err != nil {
				log.Error(err, "could not check if active master exists")
				return ctrl.Result{}, err
			}

			if !exists {
				log.Info("Master does not exist. Configuring Replication")
				if err := configureReplication(ctx, r.Client, df); err != nil {
					log.Error(err, "couldn't find healthy and mark active")
					return ctrl.Result{}, err
				}

			} else {
				log.Info(fmt.Sprintf("Master exists. Marking %s as replica", pod.Status.PodIP))
				if err := configureReplicaFromDF(ctx, r.Client, pod, df); err != nil {
					log.Error(err, "could not mark replica from db")
					return ctrl.Result{}, err
				}
			}

		}

	} else {
		log.Info("Role exists already", "role", role)
		// Pod already has a role
		// Check if there was a unhealthy event.
		// Role is present, Check for healthiness
		if role == resources.Master {
			// Master pod event. Check for health and do a failover
			if pod.Status.Phase != corev1.PodRunning {
				log.Info("Master pod is not running")
				// Pod is not running, Check if its a deletion event
				if err := configureReplication(ctx, r.Client, df); err != nil {
					log.Error(err, "couldn't find healthy and mark active")
					return ctrl.Result{}, err
				}
			}
		} else if role == resources.Replica {
			if pod.Status.Phase != corev1.PodRunning {
				// Mark it as a replica again
				if err := configureReplicaFromDF(ctx, r.Client, pod, df); err != nil {
					log.Error(err, "couldn't mark replica")
					return ctrl.Result{}, err
				}
			}
		} else {
			log.Error(errors.New("unknown role"), "unknown role")
		}
	}

	return ctrl.Result{}, nil
}

func (r *HealthReconciler) GetDFFromPod(ctx context.Context, pod *corev1.Pod) (*dfv1alpha1.Dragonfly, error) {
	dfName, ok := pod.Labels["app"]
	if !ok {
		return nil, errors.New("can't find the `app` label")
	}

	// Retrieve the relevant Dragonfly object
	var df dfv1alpha1.Dragonfly
	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      dfName,
		Namespace: pod.Namespace,
	}, &df)
	if err != nil {
		return nil, err
	}

	return &df, nil
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
