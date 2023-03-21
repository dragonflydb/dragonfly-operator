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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
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
	// TODO: Handle updates to the Dragonfly object
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

		log.Info("Waiting for the statefulset to be ready")
		// TODO: What happens if we timed out here and got the same instance event again
		if err := waitForStatefulSetReady(ctx, r.Client, df.Name, df.Namespace, 5*time.Minute); err != nil {
			log.Error(err, "could not wait for statefulset to be ready")
			return ctrl.Result{}, err
		}

		// Update Status
		df.Status.Phase = PhaseResourcesCreated
		log.Info("Created resources for object")
		if err := r.Status().Update(ctx, &df); err != nil {
			log.Error(err, "could not update the Dragonfly object")
			return ctrl.Result{}, err
		}

		r.EventRecorder.Event(&df, corev1.EventTypeNormal, "Created", "Created resources for Dragonfly object")
	} else if df.Status.Phase == PhaseReady {
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

		log.Info("Waiting for the statefulset to be ready")
		if err := waitForStatefulSetReady(ctx, r.Client, df.Name, df.Namespace, 2*time.Minute); err != nil {
			log.Error(err, "could not wait for statefulset to be ready")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DragonflyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dfv1alpha1.Dragonfly{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
