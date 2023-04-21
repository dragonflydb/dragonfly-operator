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

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// DragonflyReconciler reconciles a Dragonfly object
type DragonflyReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	EventRecorder record.EventRecorder
}

//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflies/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *DragonflyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	var df dfv1alpha1.Dragonfly
	if err := r.Get(ctx, req.NamespacedName, &df); err != nil {
		log.Info(fmt.Sprintf("could not get the Dragonfly object: %s", req.NamespacedName))
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling Dragonfly object")
	// Ignore if resource is already created
	if df.Status.Phase == "" {
		log.Info("Creating resources")
		resources, err := resources.GetDragonflyResources(ctx, &df)
		if err != nil {
			log.Error(err, "could not get resources")
			return ctrl.Result{}, err
		}

		// create all resources
		for _, resource := range resources {
			if err := r.Create(ctx, resource); err != nil {
				log.Error(err, fmt.Sprintf("could not create resource %s/%s/%s", resource.GetObjectKind(), resource.GetNamespace(), resource.GetName()))
				return ctrl.Result{}, err
			}
		}

		// Update Status
		df.Status.Phase = PhaseResourcesCreated
		log.Info("Created resources for object")
		if err := r.Status().Update(ctx, &df); err != nil {
			log.Error(err, "could not update the Dragonfly object")
			return ctrl.Result{}, err
		}

		r.EventRecorder.Event(&df, corev1.EventTypeNormal, "Resources", "Created resources")
		return ctrl.Result{}, nil
	} else if df.Status.Phase == PhaseRollout {
		// This is a Rollout
		// Get all the pods that are not on the latest spec
		// and delete them one by one
		log.Info("Rolling out new version")
		var updatedStatefulset appsv1.StatefulSet
		if err := r.Get(ctx, client.ObjectKey{Namespace: df.Namespace, Name: df.Name}, &updatedStatefulset); err != nil {
			log.Error(err, "could not get statefulset")
			return ctrl.Result{Requeue: true}, err
		}

		// get pods of the statefulset
		var pods corev1.PodList
		if err := r.List(ctx, &pods, client.InNamespace(df.Namespace), client.MatchingLabels(map[string]string{
			"app":                               df.Name,
			resources.KubernetesAppNameLabelKey: "dragonfly",
		})); err != nil {
			log.Error(err, "could not list pods")
			return ctrl.Result{Requeue: true}, err
		}

		if len(pods.Items) != int(*updatedStatefulset.Spec.Replicas) {
			log.Info("Waiting for all replicas to be ready")
			return ctrl.Result{Requeue: true}, nil
		}

		// filter replicas
		var master corev1.Pod
		replicas := make([]corev1.Pod, 0)
		for _, pod := range pods.Items {
			if _, ok := pod.Labels[resources.Role]; ok {
				if pod.Labels[resources.Role] == resources.Replica {
					replicas = append(replicas, pod)
				} else if pod.Labels[resources.Role] == resources.Master {
					master = pod
				}
			} else {
				log.Info("found pod without label", "pod", pod.Name)
				// retry after they are ready
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}

		// Check if latest replicas had a full sync
		// if not, requeue
		fullSyncedUpdatedReplicas := 0
		for _, replica := range replicas {
			// Check only with latest replicas
			onLatestVersion, err := isPodOnLatestVersion(ctx, r.Client, &replica, &updatedStatefulset)
			if err != nil {
				log.Error(err, "could not check if pod is on latest version")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}
			if onLatestVersion {
				// check if the replica had a full sync
				log.Info("New Replica found. Checking if replica had a full sync", "pod", replica.Name)
				err := checkForStableSync(ctx, r.Client, &replica)
				if err != nil {
					log.Info("Not all new replicas are in stable sync yet", "pod", replica.Name, "reason", err)
					return ctrl.Result{RequeueAfter: 5 * time.Second}, err
				}
				log.Info("Replica is in stable sync", "pod", replica.Name)
				fullSyncedUpdatedReplicas++
			}
		}

		log.Info(fmt.Sprintf("%d/%d new replicas are in stable sync", fullSyncedUpdatedReplicas, len(replicas)))

		// if we are here it means that all latest replicas are in stable sync
		// delete replicas whose spec doesn't match
		// redo: this to a method that returns next pod to rollout
		for _, replica := range replicas {
			// Check if pod is on latest version
			onLatestVersion, err := isPodOnLatestVersion(ctx, r.Client, &replica, &updatedStatefulset)
			if err != nil {
				log.Error(err, "could not check if pod is on latest version")
				return ctrl.Result{RequeueAfter: 5 * time.Second}, err
			}

			if !onLatestVersion {
				// delete the replica
				log.Info("deleting replica", "pod", replica.Name)
				r.EventRecorder.Event(&df, corev1.EventTypeNormal, "Rollout", fmt.Sprintf("Deleting replica %s", replica.Name))
				if err := r.Delete(ctx, &replica); err != nil {
					log.Error(err, "could not delete pod")
					return ctrl.Result{RequeueAfter: 5 * time.Second}, err
				}

				// Requeue and wait for it to be stable sync
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
		}

		// If we are here it means that all replicas
		// are on latest version
		// Update Master now
		log.Info("deleting master")
		if err := r.Delete(ctx, &master); err != nil {
			log.Error(err, "could not delete master")
			return ctrl.Result{}, err
		}
		r.EventRecorder.Event(&df, corev1.EventTypeNormal, "Rollout", fmt.Sprintf("Deleting master %s", master.Name))
		return ctrl.Result{}, nil
	} else {
		// This is an Update
		log.Info("updating existing resources")
		newResources, err := resources.GetDragonflyResources(ctx, &df)
		if err != nil {
			log.Error(err, "could not get resources")
			return ctrl.Result{}, err
		}

		// update all resources
		for _, resource := range newResources {
			if err := r.Update(ctx, resource); err != nil {
				log.Error(err, fmt.Sprintf("could not update resource %s/%s/%s", resource.GetObjectKind(), resource.GetNamespace(), resource.GetName()))
				return ctrl.Result{}, err
			}
		}

		log.Info("Updated resources for object")

		// perform a rollout only if the pod spec has changed
		var statefulSet appsv1.StatefulSet
		if err := r.Get(ctx, client.ObjectKey{Namespace: df.Namespace, Name: df.Name}, &statefulSet); err != nil {
			log.Error(err, "could not get statefulset")
			return ctrl.Result{}, err
		}

		// check if the spec has changed
		if statefulSet.Status.UpdatedReplicas != statefulSet.Status.Replicas {
			// if !reflect.DeepEqual(oldStatefulset.Spec, newStatefulset.Spec) {
			log.Info("Pod spec has changed, performing a rollout")
			r.EventRecorder.Event(&df, corev1.EventTypeNormal, "Rollout", "Starting a rollout")

			// Start rollout and update status
			// update status so that we can track progress
			df.Status.Phase = PhaseRollout
			if err := r.Status().Update(ctx, &df); err != nil {
				log.Error(err, "could not update the Dragonfly object")
				return ctrl.Result{Requeue: true}, err
			}

			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		r.EventRecorder.Event(&df, corev1.EventTypeNormal, "Resources", "Updated resources")
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DragonflyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Listen only to spec changes
		For(&dfv1alpha1.Dragonfly{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
