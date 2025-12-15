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
	"strings"
	"time"

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// DragonflyReconciler reconciles a Dragonfly object
type DragonflyReconciler struct {
	Reconciler
}

//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflies/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=autoscaling,resources=horizontalpodautoscalers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
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

	log.Info("reconciling dragonfly instance")

	dfi, err := r.getDragonflyInstance(ctx, req.NamespacedName, log)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(fmt.Errorf("failed to get dragonfly instance: %w", err))
	}

	if dfi.isTerminating() {
		// Ignore dragonfly instance that is being foreground deleted
		return ctrl.Result{}, nil
	}

	if err = dfi.reconcileResources(ctx); err != nil {
		if strings.Contains(err.Error(), "HPA deletion initiated") {
			log.Info("requeuing to verify HPA deletion")
			return ctrl.Result{RequeueAfter: time.Second * 2}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to reconcile dragonfly resources: %w", err)
	}

	dfiStatus := dfi.getStatus()

	if dfiStatus.Phase == PhaseReady || dfiStatus.Phase == PhaseReadyOld {
		dfiStatus, err = dfi.detectRollingUpdate(ctx)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to detect rolling update: %w", err)
		}
	}

	if dfiStatus.Phase == PhaseRollingUpdate || dfiStatus.IsRollingUpdate {
		log.Info("rolling out new version")

		statefulSet, err := dfi.getStatefulSet(ctx)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get statefulset: %w", err)
		}

		if result, err := dfi.allPodsHealthy(ctx, statefulSet.Status.UpdateRevision); !result.IsZero() || err != nil {
			return result, err
		}

		replicas, err := dfi.getReplicas(ctx)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get replicas: %w", err)
		}

		// We want to update the replicas first then the master
		// We want to have at most one updated replica in full sync phase at a time
		// if not, requeue
		if result, err := dfi.verifyUpdatedReplicas(ctx, replicas, statefulSet.Status.UpdateRevision); !result.IsZero() || err != nil {
			return result, err
		}

		// if we are here it means that all latest replicas are in stable sync
		// delete older version replicas
		if result, err := dfi.updatedReplicas(ctx, replicas, statefulSet.Status.UpdateRevision); !result.IsZero() || err != nil {
			return result, err
		}

		master, err := dfi.getMaster(ctx)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get master: %w", err)
		}

		if !isPodOnLatestVersion(master, statefulSet.Status.UpdateRevision) {
			if err = dfi.updatedMaster(ctx, master, replicas, statefulSet.Status.UpdateRevision); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update master: %w", err)
			}

			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		} else {
			if err = dfi.detectOldMasters(ctx, statefulSet.Status.UpdateRevision); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to detect old masters: %w", err)
			}
		}

		// If we are here all are on latest version
		dfiStatus.Phase = PhaseReady
		// TODO: remove this in a future release.
		dfiStatus.IsRollingUpdate = false
		if err = dfi.patchStatus(ctx, dfiStatus); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update the dragonfly status: %w", err)
		}

		dfi.eventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Rollout", "Completed")
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DragonflyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Listen only to spec changes
		For(&dfv1alpha1.Dragonfly{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.StatefulSet{}, builder.MatchEveryOwner).
		Owns(&corev1.Service{}, builder.MatchEveryOwner).
		Owns(&autoscalingv2.HorizontalPodAutoscaler{}, builder.MatchEveryOwner).
		Named("Dragonfly").
		Complete(r)
}
