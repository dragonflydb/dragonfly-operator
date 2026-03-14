/*
Copyright 2026 DragonflyDB authors.

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
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/go-logr/logr"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// DragonflyClusterReconciler reconciles a DragonflyCluster object.
type DragonflyClusterReconciler struct {
	Reconciler
}

const (
	dfcPhaseReconciling = "Reconciling"
	dfcPhaseReady       = "Ready"
	dfcPhaseRebalancing = "Rebalancing"
	dfcPhaseError       = "Error"
)

const (
	dfcShardNameFormat = "%s-shard-%d"
	dfcLabelClusterKey = "operator.dragonflydb.io/cluster"
	dfcLabelShardKey   = "operator.dragonflydb.io/shard"
)

const (
	dfcCondReady       = "Ready"
	dfcCondRebalancing = "Rebalancing"
	dfcCondDegraded    = "Degraded"
)

//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflyclusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflyclusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflyclusters/finalizers,verbs=update
//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=dragonflydb.io,resources=dragonflies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *DragonflyClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling dragonflycluster", "request", req.NamespacedName)

	var cluster dfv1alpha1.DragonflyCluster
	if err := r.Client.Get(ctx, req.NamespacedName, &cluster); err != nil {
		logger.Error(err, "failed to get dragonflycluster", "request", req.NamespacedName)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	prevPhase := cluster.Status.Phase

	if cluster.DeletionTimestamp != nil {
		// No finalizers yet. Child Dragonfly CRs are owned and will be GC'd.
		return ctrl.Result{}, nil
	}

	// Set a default rebalance config.
	rebalance := cluster.Spec.Rebalance
	if rebalance == nil {
		rebalance = &dfv1alpha1.DragonflyClusterRebalanceSpec{
			Enabled:                 true,
			MaxSlotsPerMigration:    1024,
			MaxConcurrentMigrations: 1,
		}
	}

	// Ensure shard Dragonfly CRs exist.
	if err := r.reconcileShardDragonflies(ctx, logger, &cluster); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileShardDragonflies: %w", err)
	}

	// Ensure a stable bootstrap service exists for clients to connect to any master.
	if err := r.reconcileClusterService(ctx, logger, &cluster); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcileClusterService: %w", err)
	}

	// Wait for all shard pods to be ready and have roles assigned.
	shards, notReadyReason, err := r.collectShardTopology(ctx, logger, &cluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("collectShardTopology: %w", err)
	}
	if notReadyReason != "" {
		if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
			setPhaseAndConditions(st, dfcPhaseReconciling, cluster.Generation, "WaitingForShards", notReadyReason)
		}); err != nil {
			return ctrl.Result{}, err
		}
		if prevPhase != dfcPhaseReconciling {
			r.EventRecorder.Eventf(&cluster, corev1.EventTypeNormal, "Reconciling", "Waiting for shards to be ready: %s", notReadyReason)
		}
		logger.Info("waiting for shards to be ready", "reason", notReadyReason)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// This controller currently supports scale-out only.
	if len(cluster.Status.Shards) > int(cluster.Spec.Shards) {
		if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
			setPhaseAndConditions(st, dfcPhaseError, cluster.Generation, "ScaleInNotSupported", "Scale-in is not supported yet")
		}); err != nil {
			return ctrl.Result{}, err
		}
		if prevPhase != dfcPhaseError {
			r.EventRecorder.Eventf(&cluster, corev1.EventTypeWarning, "ScaleInNotSupported", "Scale-in is not supported yet (status has %d shards, spec requests %d)", len(cluster.Status.Shards), cluster.Spec.Shards)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("scale-in is not supported yet (status has %d shards, spec requests %d)", len(cluster.Status.Shards), cluster.Spec.Shards)
	}

	// Ensure status has entries for all shards (new shards start with 0 slots).
	if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
		existing := make(map[int32]dfv1alpha1.DragonflyClusterShardStatus, len(st.Shards))
		for _, ss := range st.Shards {
			existing[ss.ShardIndex] = ss
		}
		next := make([]dfv1alpha1.DragonflyClusterShardStatus, 0, int(cluster.Spec.Shards))
		for i := int32(0); i < cluster.Spec.Shards; i++ {
			ss, ok := existing[i]
			if !ok {
				ss = dfv1alpha1.DragonflyClusterShardStatus{
					ShardIndex:    i,
					DragonflyName: fmt.Sprintf(dfcShardNameFormat, cluster.Name, i),
					Slots:         0,
					SlotRanges:    nil,
				}
			}
			ss.DragonflyName = fmt.Sprintf(dfcShardNameFormat, cluster.Name, i)
			// Update master pod name for observability.
			for _, s := range shards {
				if s.ShardIndex == i && s.MasterPod != nil {
					ss.MasterPodName = s.MasterPod.Name
				}
			}
			ss.Slots = int32(slotRangesSize(ss.SlotRanges))
			next = append(next, ss)
		}
		st.Shards = next
		st.ObservedGeneration = cluster.Generation
	}); err != nil {
		return ctrl.Result{}, err
	}

	// Ensure we have an initial slot assignment recorded in status.
	// If no shard owns any slots yet, initialize to an even distribution.
	if statusTotalSlots(cluster.Status.Shards) == 0 {
		initial := evenSlotRanges(cluster.Spec.Shards)
		if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
			setPhaseAndConditions(st, dfcPhaseReconciling, cluster.Generation, "InitializingSlots", "Initializing slot ranges to an even distribution")
			st.Shards = make([]dfv1alpha1.DragonflyClusterShardStatus, 0, int(cluster.Spec.Shards))
			for i := int32(0); i < cluster.Spec.Shards; i++ {
				name := fmt.Sprintf(dfcShardNameFormat, cluster.Name, i)
				ranges := initial[i]
				st.Shards = append(st.Shards, dfv1alpha1.DragonflyClusterShardStatus{
					ShardIndex:    i,
					DragonflyName: name,
					Slots:         int32(slotRangesSize(ranges)),
					SlotRanges:    ranges,
				})
			}
		}); err != nil {
			return ctrl.Result{}, err
		}
		r.EventRecorder.Eventf(&cluster, corev1.EventTypeNormal, "InitializingSlots", "Initialized slot ranges for %d shards", cluster.Spec.Shards)
	}

	if err := validateSlotCoverage(cluster.Status.Shards, cluster.Spec.Shards); err != nil {
		logger.Error(err, "invalid slot coverage in status; refusing to push cluster config")
		if err2 := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
			setPhaseAndConditions(st, dfcPhaseError, cluster.Generation, "InvalidSlotCoverage", err.Error())
		}); err2 != nil {
			return ctrl.Result{}, err2
		}
		if prevPhase != dfcPhaseError {
			r.EventRecorder.Eventf(&cluster, corev1.EventTypeWarning, "InvalidSlotCoverage", "%s", err.Error())
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Build desired cluster config using:
	// - current role/master selection (from shard pods)
	// - current slot ownership (from status)
	// - optional current migration (from status)
	cfg, err := r.buildClusterConfig(&cluster, shards)
	if err != nil {
		return ctrl.Result{}, err
	}

	// If a migration is recorded, track it and finalize when finished.
	if cluster.Status.Migration != nil {
		// Ensure the shard masters we started the migration with still match.
		// If masters changed before migration started, it's safe to restart the migration.
		if restart, err := r.handleMigrationTopologyChange(ctx, logger, &cluster, shards); err != nil {
			return ctrl.Result{}, err
		} else if restart {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}

		// Ensure we have a start timestamp for timeout handling.
		if cluster.Status.Migration.StartedAt == nil {
			now := metav1.Now()
			if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
				if st.Migration != nil && st.Migration.StartedAt == nil {
					st.Migration.StartedAt = &now
				}
			}); err != nil {
				return ctrl.Result{}, err
			}
		}

		state, lastErr, pollErr := r.pollMigration(ctx, shards, cluster.Status.Migration)
		if pollErr != nil {
			logger.Error(pollErr, "failed polling migration")
			if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
				setPhaseAndConditions(st, dfcPhaseError, cluster.Generation, "MigrationPollFailed", pollErr.Error())
				st.Migration.State = "ERROR"
				st.Migration.LastError = pollErr.Error()
			}); err != nil {
				return ctrl.Result{}, err
			}
			if prevPhase != dfcPhaseError {
				r.EventRecorder.Eventf(&cluster, corev1.EventTypeWarning, "MigrationPollFailed", "%s", pollErr.Error())
			}
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		// Timeout handling: stuck migrations are surfaced as errors.
		if cluster.Status.Migration.StartedAt != nil {
			elapsed := time.Since(cluster.Status.Migration.StartedAt.Time)
			if (state == "NOT_STARTED" || state == "NOT_FOUND") && elapsed > 2*time.Minute {
				timeoutErr := fmt.Errorf("migration did not start within %s (state=%s)", elapsed.Round(time.Second), state)
				logger.Error(timeoutErr, "migration start timeout")
				if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
					setPhaseAndConditions(st, dfcPhaseError, cluster.Generation, "MigrationStartTimeout", timeoutErr.Error())
					if st.Migration != nil {
						st.Migration.State = state
						st.Migration.LastError = timeoutErr.Error()
					}
				}); err != nil {
					return ctrl.Result{}, err
				}
				if prevPhase != dfcPhaseError {
					r.EventRecorder.Eventf(&cluster, corev1.EventTypeWarning, "MigrationStartTimeout", "%s", timeoutErr.Error())
				}
				return ctrl.Result{RequeueAfter: 30 * time.Second}, timeoutErr
			}
			if state != "FINISHED" && elapsed > 30*time.Minute {
				timeoutErr := fmt.Errorf("migration exceeded timeout (%s, state=%s)", elapsed.Round(time.Second), state)
				logger.Error(timeoutErr, "migration timeout")
				if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
					setPhaseAndConditions(st, dfcPhaseError, cluster.Generation, "MigrationTimeout", timeoutErr.Error())
					if st.Migration != nil {
						st.Migration.State = state
						st.Migration.LastError = timeoutErr.Error()
					}
				}); err != nil {
					return ctrl.Result{}, err
				}
				if prevPhase != dfcPhaseError {
					r.EventRecorder.Eventf(&cluster, corev1.EventTypeWarning, "MigrationTimeout", "%s", timeoutErr.Error())
				}
				return ctrl.Result{RequeueAfter: 30 * time.Second}, timeoutErr
			}
		}

		if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
			setPhaseAndConditions(st, dfcPhaseRebalancing, cluster.Generation, "MigrationInProgress", fmt.Sprintf("Migration state=%s", state))
			st.Migration.State = state
			st.Migration.LastError = lastErr
		}); err != nil {
			return ctrl.Result{}, err
		}

		switch state {
		case "FINISHED":
			mig := *cluster.Status.Migration
			// Apply the slot range move to our status model, then push a config without migrations.
			if err := r.applyMigrationResult(ctx, logger, &cluster); err != nil {
				return ctrl.Result{}, err
			}
			// Rebuild cfg after updating status (slot ownership changes, migration removed)
			if err := r.Client.Get(ctx, req.NamespacedName, &cluster); err != nil {
				return ctrl.Result{}, err
			}
			cfg, err = r.buildClusterConfig(&cluster, shards)
			if err != nil {
				return ctrl.Result{}, err
			}
			if err := r.pushClusterConfig(ctx, logger, shards, cfg); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
				setPhaseAndConditions(st, dfcPhaseReconciling, cluster.Generation, "MigrationFinished", "Migration finished, updating slot ownership")
				st.Migration = nil
			}); err != nil {
				return ctrl.Result{}, err
			}
			r.EventRecorder.Eventf(&cluster, corev1.EventTypeNormal, "MigrationFinished", "Finished migrating slots [%d,%d] from shard %d to shard %d", mig.SlotStart, mig.SlotEnd, mig.SourceShard, mig.TargetShard)
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		case "ERROR", "FATAL":
			// Stop and surface error; operator won't try new migrations automatically.
			logger.Info("migration failed", "state", state, "error", lastErr)
			if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
				setPhaseAndConditions(st, dfcPhaseError, cluster.Generation, "MigrationFailed", fmt.Sprintf("Migration failed (state=%s): %s", state, lastErr))
			}); err != nil {
				return ctrl.Result{}, err
			}
			if prevPhase != dfcPhaseError {
				r.EventRecorder.Eventf(&cluster, corev1.EventTypeWarning, "MigrationFailed", "Migration failed (state=%s): %s", state, lastErr)
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		default:
			// Still running; keep config pushed (with migrations).
			if err := r.pushClusterConfig(ctx, logger, shards, cfg); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// No migration currently in progress.
	// Push current config (idempotent; Dragonfly returns OK if same).
	if err := r.pushClusterConfig(ctx, logger, shards, cfg); err != nil {
		return ctrl.Result{}, err
	}

	// If rebalancing is enabled, decide if we should start a new migration.
	if rebalance.Enabled && rebalance.MaxConcurrentMigrations == 1 {
		next, ok := chooseRebalanceMigration(&cluster, rebalance.MaxSlotsPerMigration)
		if ok {
			now := metav1.Now()
			next.StartedAt = &now
			// Pin source/target identities for robustness during migration.
			for _, s := range shards {
				if s.ShardIndex == next.SourceShard && s.MasterPod != nil {
					next.SourceNodeID = s.MasterNodeID
				}
				if s.ShardIndex == next.TargetShard && s.MasterPod != nil {
					next.TargetNodeID = s.MasterNodeID
					next.TargetIP = s.MasterPod.Status.PodIP
				}
			}
			logger.Info("starting rebalance migration",
				"sourceShard", next.SourceShard,
				"targetShard", next.TargetShard,
				"slotStart", next.SlotStart,
				"slotEnd", next.SlotEnd,
			)
			if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
				setPhaseAndConditions(st, dfcPhaseRebalancing, cluster.Generation, "MigrationStarted", fmt.Sprintf("Starting migration state=%s", next.State))
				st.Migration = &next
			}); err != nil {
				return ctrl.Result{}, err
			}
			r.EventRecorder.Eventf(&cluster, corev1.EventTypeNormal, "MigrationStarted", "Migrating slots [%d,%d] from shard %d to shard %d", next.SlotStart, next.SlotEnd, next.SourceShard, next.TargetShard)

			// Rebuild config with migration included and push.
			if err := r.Client.Get(ctx, req.NamespacedName, &cluster); err != nil {
				return ctrl.Result{}, err
			}
			cfg, err := r.buildClusterConfig(&cluster, shards)
			if err != nil {
				return ctrl.Result{}, err
			}
			if err := r.pushClusterConfig(ctx, logger, shards, cfg); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	if err := r.patchClusterStatus(ctx, &cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
		setPhaseAndConditions(st, dfcPhaseReady, cluster.Generation, "Ready", "Cluster is ready")
	}); err != nil {
		return ctrl.Result{}, err
	}
	if prevPhase != dfcPhaseReady {
		r.EventRecorder.Eventf(&cluster, corev1.EventTypeNormal, "Ready", "DragonflyCluster is ready")
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *DragonflyClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapPodToCluster := func(ctx context.Context, obj client.Object) []ctrl.Request {
		clusterName := obj.GetLabels()[dfcLabelClusterKey]
		if clusterName == "" {
			return nil
		}
		return []ctrl.Request{{NamespacedName: types.NamespacedName{Name: clusterName, Namespace: obj.GetNamespace()}}}
	}

	podHasClusterLabel := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetLabels()[dfcLabelClusterKey] != ""
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&dfv1alpha1.DragonflyCluster{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&dfv1alpha1.Dragonfly{}).
		Owns(&corev1.Service{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(mapPodToCluster), builder.WithPredicates(podHasClusterLabel)).
		Named("DragonflyCluster").
		Complete(r)
}

func (r *DragonflyClusterReconciler) reconcileClusterService(ctx context.Context, logger logr.Logger, cluster *dfv1alpha1.DragonflyCluster) error {
	svcName := cluster.Name
	var svc corev1.Service
	err := r.Client.Get(ctx, types.NamespacedName{Name: svcName, Namespace: cluster.Namespace}, &svc)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get Service %s/%s: %w", cluster.Namespace, svcName, err)
	}

	desired := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				dfcLabelClusterKey: cluster.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				dfcLabelClusterKey:                  cluster.Name,
				resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
				resources.RoleLabelKey:              resources.Master,
			},
			Ports: []corev1.ServicePort{
				{
					Name: resources.DragonflyPortName,
					Port: resources.DragonflyPort,
				},
			},
			Type: corev1.ServiceTypeClusterIP,
		},
	}

	if apierrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(cluster, &desired, r.Scheme); err != nil {
			return fmt.Errorf("set controller reference on Service %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		if err := r.Client.Create(ctx, &desired); err != nil {
			return fmt.Errorf("create Service %s/%s: %w", desired.Namespace, desired.Name, err)
		}
		logger.Info("created dragonflycluster service", "service", desired.Name)
		r.EventRecorder.Eventf(cluster, corev1.EventTypeNormal, "ServiceCreated", "Created bootstrap Service %s", desired.Name)
		return nil
	}

	patchFrom := client.MergeFrom(svc.DeepCopy())

	// Update in place (patch the existing object) to avoid subtle issues when patching
	// a freshly-constructed Service object.
	if svc.Labels == nil {
		svc.Labels = map[string]string{}
	}
	svc.Labels[dfcLabelClusterKey] = cluster.Name
	svc.Spec.Selector = desired.Spec.Selector
	svc.Spec.Ports = desired.Spec.Ports
	svc.Spec.Type = desired.Spec.Type

	if err := controllerutil.SetControllerReference(cluster, &svc, r.Scheme); err != nil {
		return fmt.Errorf("set controller reference on Service %s/%s: %w", svc.Namespace, svc.Name, err)
	}
	if err := r.Client.Patch(ctx, &svc, patchFrom); err != nil {
		return fmt.Errorf("patch Service %s/%s: %w", svc.Namespace, svc.Name, err)
	}
	return nil
}

// reconcileShardDragonflies ensures per-shard Dragonfly CRs exist and are up to date.
func (r *DragonflyClusterReconciler) reconcileShardDragonflies(ctx context.Context, logger logr.Logger, cluster *dfv1alpha1.DragonflyCluster) error {
	for i := int32(0); i < cluster.Spec.Shards; i++ {
		dfName := fmt.Sprintf(dfcShardNameFormat, cluster.Name, i)
		var df dfv1alpha1.Dragonfly
		err := r.Client.Get(ctx, types.NamespacedName{Name: dfName, Namespace: cluster.Namespace}, &df)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}

		desired := dfv1alpha1.Dragonfly{
			ObjectMeta: metav1.ObjectMeta{
				Name:      dfName,
				Namespace: cluster.Namespace,
				Labels: map[string]string{
					dfcLabelClusterKey: cluster.Name,
					dfcLabelShardKey:   strconv.Itoa(int(i)),
				},
			},
		}

		// Apply template (full DragonflySpec). We deep-copy to avoid mutating the CR's spec while normalizing.
		desired.Spec = *cluster.Spec.Template.DeepCopy()

		// Make snapshot dir per-shard to avoid filename collisions when using S3.
		// Each shard gets its own prefix: e.g. s3://bucket/falcon/shard-0, s3://bucket/falcon/shard-1, etc.
		if desired.Spec.Snapshot != nil && desired.Spec.Snapshot.Dir != "" {
			desired.Spec.Snapshot.Dir = fmt.Sprintf("%s/shard-%d", strings.TrimRight(desired.Spec.Snapshot.Dir, "/"), i)
		}

		// Ensure labels propagate to pods and include cluster/shard identifiers.
		if desired.Spec.Labels == nil {
			desired.Spec.Labels = map[string]string{}
		}
		desired.Spec.Labels[dfcLabelClusterKey] = cluster.Name
		desired.Spec.Labels[dfcLabelShardKey] = strconv.Itoa(int(i))

		// ServiceSpec.Name must be unique per shard; if a template specifies a name, drop it so we fall back to df.Name.
		if desired.Spec.ServiceSpec != nil && desired.Spec.ServiceSpec.Name != "" {
			svc := desired.Spec.ServiceSpec.DeepCopy()
			svc.Name = ""
			desired.Spec.ServiceSpec = svc
		}

		// Replicas per shard: 1 master + replicasPerShard.
		desired.Spec.Replicas = 1 + cluster.Spec.ReplicasPerShard

		// Ensure cluster mode is enabled.
		desired.Spec.Args = removeArgsWithPrefix(desired.Spec.Args, "--cluster_mode")
		desired.Spec.Args = append(desired.Spec.Args, "--cluster_mode=yes")

		// Ensure stable node IDs across restarts by setting env var (Dragonfly supports DFLY_<flag> env override).
		desired.Spec.Env = upsertEnvVar(desired.Spec.Env, corev1.EnvVar{
			Name: "DFLY_cluster_node_id",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		})

		// Create or update.
		if apierrors.IsNotFound(err) {
			if err := controllerutil.SetControllerReference(cluster, &desired, r.Scheme); err != nil {
				return err
			}
			if err := r.Client.Create(ctx, &desired); err != nil {
				return err
			}
			logger.Info("created shard dragonfly", "dragonfly", desired.Name)
			r.EventRecorder.Eventf(cluster, corev1.EventTypeNormal, "ShardCreated", "Created shard Dragonfly %s", desired.Name)
			continue
		}

		// Update existing spec to desired if changed. Keep it simple: update Spec wholesale.
		patchFrom := client.MergeFrom(df.DeepCopy())
		df.Spec = desired.Spec
		if df.Labels == nil {
			df.Labels = map[string]string{}
		}
		df.Labels[dfcLabelClusterKey] = cluster.Name
		df.Labels[dfcLabelShardKey] = strconv.Itoa(int(i))
		if err := controllerutil.SetControllerReference(cluster, &df, r.Scheme); err != nil {
			return err
		}
		if err := r.Client.Patch(ctx, &df, patchFrom); err != nil {
			return err
		}
	}
	return nil
}

type shardRuntime struct {
	ShardIndex     int32
	Dragonfly      string
	MasterPod      *corev1.Pod
	ReplicaPods    []corev1.Pod
	MasterNodeID   string
	ReplicaNodeIDs map[string]string // podName -> nodeID
}

func (r *DragonflyClusterReconciler) collectShardTopology(ctx context.Context, logger logr.Logger, cluster *dfv1alpha1.DragonflyCluster) ([]shardRuntime, string, error) {
	shards := make([]shardRuntime, 0, int(cluster.Spec.Shards))
	for i := int32(0); i < cluster.Spec.Shards; i++ {
		dfName := fmt.Sprintf(dfcShardNameFormat, cluster.Name, i)
		pods, err := r.listDragonflyPods(ctx, cluster.Namespace, dfName)
		if err != nil {
			return nil, "", err
		}
		if len(pods.Items) == 0 {
			return nil, fmt.Sprintf("no pods for shard %s yet", dfName), nil
		}

		var master *corev1.Pod
		var replicas []corev1.Pod
		for _, p := range pods.Items {
			// We must have a ready master per shard, but we can tolerate missing/unready replicas.
			// This avoids blocking cluster reconciliation on replica scheduling churn.
			if !isHealthy(&p) {
				if isMaster(&p) {
					return nil, fmt.Sprintf("master pod %s not ready", p.Name), nil
				}
				continue
			}

			if isMaster(&p) {
				if master != nil {
					return nil, "", fmt.Errorf("multiple masters found for shard %s", dfName)
				}
				cp := p
				master = &cp
			} else if isReplica(&p) {
				replicas = append(replicas, p)
			}
		}
		if master == nil {
			return nil, fmt.Sprintf("no master elected for shard %s yet", dfName), nil
		}

		masterID, err := r.getClusterMyID(ctx, master)
		if err != nil {
			return nil, "", fmt.Errorf("failed to get node id for master %s: %w", master.Name, err)
		}

		replicaIDs := make(map[string]string, len(replicas))
		for _, rp := range replicas {
			id, err := r.getClusterMyID(ctx, &rp)
			if err != nil {
				return nil, "", fmt.Errorf("failed to get node id for replica %s: %w", rp.Name, err)
			}
			replicaIDs[rp.Name] = id
		}

		shards = append(shards, shardRuntime{
			ShardIndex:     i,
			Dragonfly:      dfName,
			MasterPod:      master,
			ReplicaPods:    replicas,
			MasterNodeID:   masterID,
			ReplicaNodeIDs: replicaIDs,
		})
	}
	return shards, "", nil
}

func (r *DragonflyClusterReconciler) listDragonflyPods(ctx context.Context, namespace, dfName string) (*corev1.PodList, error) {
	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods,
		client.InNamespace(namespace),
		client.MatchingLabels{
			resources.DragonflyNameLabelKey:     dfName,
			resources.KubernetesPartOfLabelKey:  resources.KubernetesPartOf,
			resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
		},
	); err != nil {
		return nil, err
	}
	return &pods, nil
}

func (r *DragonflyClusterReconciler) getClusterMyID(ctx context.Context, pod *corev1.Pod) (string, error) {
	c := redis.NewClient(&redis.Options{
		ClientName:            resources.DragonflyOperatorName,
		Addr:                  net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
		DialTimeout:           5 * time.Second,
		ReadTimeout:           10 * time.Second,
		WriteTimeout:          10 * time.Second,
		ContextTimeoutEnabled: true,
		MaintNotificationsConfig: &maintnotifications.Config{
			Mode: maintnotifications.ModeDisabled,
		},
	})
	defer c.Close()
	var res string
	err := doWithRetry(ctx, 5, 200*time.Millisecond, func(opCtx context.Context) error {
		v, err := c.Do(opCtx, "CLUSTER", "MYID").Text()
		if err != nil {
			return err
		}
		res = v
		return nil
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res), nil
}

type dflyClusterConfig struct {
	SlotRanges []dflySlotRange      `json:"slot_ranges"`
	Master     dflyNode             `json:"master"`
	Replicas   []dflyNode           `json:"replicas"`
	Migrations []dflyMigrationEntry `json:"migrations,omitempty"`
}

type dflySlotRange struct {
	Start int32 `json:"start"`
	End   int32 `json:"end"`
}

type dflyNode struct {
	ID   string `json:"id"`
	IP   string `json:"ip"`
	Port int32  `json:"port"`
}

type dflyMigrationEntry struct {
	SlotRanges []dflySlotRange `json:"slot_ranges"`
	NodeID     string          `json:"node_id"`
	IP         string          `json:"ip"`
	Port       int32           `json:"port"`
}

func (r *DragonflyClusterReconciler) buildClusterConfig(cluster *dfv1alpha1.DragonflyCluster, shards []shardRuntime) ([]dflyClusterConfig, error) {
	byIndex := make(map[int32]shardRuntime, len(shards))
	for _, s := range shards {
		byIndex[s.ShardIndex] = s
	}

	owned := make(map[int32][]dfv1alpha1.DragonflyClusterSlotRange, len(cluster.Status.Shards))
	for _, ss := range cluster.Status.Shards {
		owned[ss.ShardIndex] = append([]dfv1alpha1.DragonflyClusterSlotRange{}, ss.SlotRanges...)
	}

	cfg := make([]dflyClusterConfig, 0, int(cluster.Spec.Shards))
	// By default, we advertise in-cluster service DNS names (`*.svc`). For clients in a
	// different cluster or network, override with DRAGONFLY_CLUSTER_SERVICE_SUFFIX so
	// redirects use e.g. `dragonfly-shard-N.<ns>.svc.example.com` instead.
	advertiseSvcSuffix := strings.TrimSpace(os.Getenv("DRAGONFLY_CLUSTER_SERVICE_SUFFIX"))
	if advertiseSvcSuffix == "" {
		advertiseSvcSuffix = "svc"
	}
	advertiseSvcSuffix = strings.TrimPrefix(advertiseSvcSuffix, ".")
	advertiseSvcSuffix = strings.TrimSuffix(advertiseSvcSuffix, ".")

	for i := int32(0); i < cluster.Spec.Shards; i++ {
		s, ok := byIndex[i]
		if !ok {
			return nil, fmt.Errorf("missing runtime for shard %d", i)
		}
		// Advertise per-pod ClusterIP service DNS names instead of pod IPs.
		// Each pod has a dedicated ClusterIP service (created by GenerateDragonflyResources)
		// named after the pod: <pod-name>.<namespace>.<svc-suffix>.
		// ClusterIPs (e.g. 10.37.x.x) are routable cross-cluster, unlike pod IPs
		// (e.g. 240.240.x.x) which only work intra-cluster. This makes CLUSTER SLOTS
		// responses usable from clients in other clusters.
		masterHost := fmt.Sprintf("%s.%s.%s", s.MasterPod.Name, cluster.Namespace, advertiseSvcSuffix)
		master := dflyNode{
			ID:   s.MasterNodeID,
			IP:   masterHost,
			Port: resources.DragonflyPort,
		}
		replicas := make([]dflyNode, 0, len(s.ReplicaPods))
		for _, rp := range s.ReplicaPods {
			replicaHost := fmt.Sprintf("%s.%s.%s", rp.Name, cluster.Namespace, advertiseSvcSuffix)
			replicas = append(replicas, dflyNode{
				ID:   s.ReplicaNodeIDs[rp.Name],
				IP:   replicaHost,
				Port: resources.DragonflyPort,
			})
		}
		sort.Slice(replicas, func(a, b int) bool { return replicas[a].ID < replicas[b].ID })

		ranges := owned[i]
		slotRanges := make([]dflySlotRange, 0, len(ranges))
		for _, sr := range ranges {
			slotRanges = append(slotRanges, dflySlotRange{Start: sr.Start, End: sr.End})
		}

		entry := dflyClusterConfig{
			SlotRanges: slotRanges,
			Master:     master,
			Replicas:   replicas,
		}

		// Include migration (outgoing) if the current migration's source is this shard.
		if cluster.Status.Migration != nil && cluster.Status.Migration.SourceShard == i {
			mig := cluster.Status.Migration
			targetNodeID := mig.TargetNodeID
			targetIP := mig.TargetIP

			// Backwards compatibility / fallback: resolve target from current runtime if status isn't pinned yet.
			if targetNodeID == "" || targetIP == "" {
				t, ok := byIndex[mig.TargetShard]
				if !ok {
					return nil, fmt.Errorf("missing runtime for target shard %d", mig.TargetShard)
				}
				targetNodeID = t.MasterNodeID
				targetIP = t.MasterPod.Status.PodIP
			}
			entry.Migrations = []dflyMigrationEntry{
				{
					SlotRanges: []dflySlotRange{{Start: mig.SlotStart, End: mig.SlotEnd}},
					NodeID:     targetNodeID,
					IP:         targetIP,
					Port:       resources.DragonflyPort,
				},
			}
		}

		cfg = append(cfg, entry)
	}

	return cfg, nil
}

func (r *DragonflyClusterReconciler) pushClusterConfig(ctx context.Context, logger logr.Logger, shards []shardRuntime, cfg []dflyClusterConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	cfgStr := string(b)

	// Push to all nodes (masters and replicas) via admin port.
	var pods []*corev1.Pod
	for _, s := range shards {
		pods = append(pods, s.MasterPod)
		for i := range s.ReplicaPods {
			cp := s.ReplicaPods[i]
			pods = append(pods, &cp)
		}
	}

	for _, pod := range pods {
		c := redis.NewClient(&redis.Options{
			ClientName:            resources.DragonflyOperatorName,
			Addr:                  net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
			DialTimeout:           5 * time.Second,
			ReadTimeout:           10 * time.Second,
			WriteTimeout:          10 * time.Second,
			ContextTimeoutEnabled: true,
			MaintNotificationsConfig: &maintnotifications.Config{
				Mode: maintnotifications.ModeDisabled,
			},
		})
		pushErr := doWithRetry(ctx, 5, 200*time.Millisecond, func(opCtx context.Context) error {
			_, err := c.Do(opCtx, "DFLYCLUSTER", "CONFIG", cfgStr).Result()
			return err
		})
		c.Close()
		if pushErr != nil {
			logger.Error(pushErr, "failed to push DFLYCLUSTER CONFIG", "pod", pod.Name)
			return pushErr
		}
	}
	return nil
}

func (r *DragonflyClusterReconciler) pollMigration(ctx context.Context, shards []shardRuntime, mig *dfv1alpha1.DragonflyClusterMigrationStatus) (state string, lastErr string, err error) {
	var src *corev1.Pod
	targetID := mig.TargetNodeID
	for _, s := range shards {
		if s.ShardIndex == mig.SourceShard {
			src = s.MasterPod
		}
	}
	if src == nil {
		return "", "", fmt.Errorf("source shard %d not found", mig.SourceShard)
	}
	if targetID == "" {
		// Fallback: resolve from current target shard master.
		for _, s := range shards {
			if s.ShardIndex == mig.TargetShard {
				targetID = s.MasterNodeID
			}
		}
	}
	if targetID == "" {
		return "", "", fmt.Errorf("target node id not found for shard %d", mig.TargetShard)
	}

	c := redis.NewClient(&redis.Options{
		ClientName:            resources.DragonflyOperatorName,
		Addr:                  net.JoinHostPort(src.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
		DialTimeout:           5 * time.Second,
		ReadTimeout:           10 * time.Second,
		WriteTimeout:          10 * time.Second,
		ContextTimeoutEnabled: true,
		MaintNotificationsConfig: &maintnotifications.Config{
			Mode: maintnotifications.ModeDisabled,
		},
	})
	defer c.Close()

	// DFLYCLUSTER SLOT-MIGRATION-STATUS returns an array of arrays:
	// [direction, node_id, state, keys_number, error]
	var raw interface{}
	err = doWithRetry(ctx, 5, 200*time.Millisecond, func(opCtx context.Context) error {
		v, err := c.Do(opCtx, "DFLYCLUSTER", "SLOT-MIGRATION-STATUS", targetID).Result()
		if err != nil {
			return err
		}
		raw = v
		return nil
	})
	if err != nil {
		return "", "", err
	}

	arr, ok := raw.([]interface{})
	if !ok {
		return "", "", fmt.Errorf("unexpected SLOT-MIGRATION-STATUS type: %T", raw)
	}
	if len(arr) == 0 {
		// Migration hasn't started yet (or source does not report it).
		return "NOT_STARTED", "", nil
	}
	for _, item := range arr {
		row, ok := item.([]interface{})
		if !ok || len(row) < 5 {
			continue
		}
		dir := fmt.Sprint(row[0])
		nodeID := fmt.Sprint(row[1])
		st := fmt.Sprint(row[2])
		errStr := fmt.Sprint(row[4])
		if dir == "out" && nodeID == targetID {
			return st, errStr, nil
		}
	}
	return "NOT_FOUND", "", nil
}

func (r *DragonflyClusterReconciler) handleMigrationTopologyChange(ctx context.Context, logger logr.Logger, cluster *dfv1alpha1.DragonflyCluster, shards []shardRuntime) (restart bool, err error) {
	mig := cluster.Status.Migration
	if mig == nil {
		return false, nil
	}

	var src shardRuntime
	var dst shardRuntime
	srcFound := false
	dstFound := false
	for _, s := range shards {
		if s.ShardIndex == mig.SourceShard {
			src = s
			srcFound = true
		}
		if s.ShardIndex == mig.TargetShard {
			dst = s
			dstFound = true
		}
	}
	if !srcFound || !dstFound {
		return false, nil
	}

	mismatch := false
	var reasons []string
	if mig.SourceNodeID != "" && src.MasterNodeID != mig.SourceNodeID {
		mismatch = true
		reasons = append(reasons, "source master changed")
	}
	if mig.TargetNodeID != "" && dst.MasterNodeID != mig.TargetNodeID {
		mismatch = true
		reasons = append(reasons, "target master changed")
	}
	if mig.TargetIP != "" && dst.MasterPod != nil && dst.MasterPod.Status.PodIP != "" && dst.MasterPod.Status.PodIP != mig.TargetIP {
		mismatch = true
		reasons = append(reasons, "target IP changed")
	}

	if !mismatch {
		return false, nil
	}

	// If we haven't started the migration yet, restart it safely.
	switch mig.State {
	case "", "STARTING", "NOT_STARTED", "NOT_FOUND":
		logger.Info("restarting migration due to topology change", "reasons", strings.Join(reasons, ", "))
		if err := r.patchClusterStatus(ctx, cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
			setPhaseAndConditions(st, dfcPhaseReconciling, cluster.Generation, "MigrationRestarted", fmt.Sprintf("Restarting migration due to topology change: %s", strings.Join(reasons, ", ")))
			st.Migration = nil
		}); err != nil {
			return false, err
		}
		r.EventRecorder.Eventf(cluster, corev1.EventTypeNormal, "MigrationRestarted", "Restarting migration due to topology change: %s", strings.Join(reasons, ", "))
		return true, nil
	default:
		// Migration already in progress; surface an error.
		err := fmt.Errorf("migration topology changed during migration (state=%s): %s", mig.State, strings.Join(reasons, ", "))
		logger.Error(err, "migration cannot continue safely")
		if err2 := r.patchClusterStatus(ctx, cluster, func(st *dfv1alpha1.DragonflyClusterStatus) {
			setPhaseAndConditions(st, dfcPhaseError, cluster.Generation, "MigrationTopologyChanged", err.Error())
			if st.Migration != nil {
				st.Migration.LastError = err.Error()
			}
		}); err2 != nil {
			return false, err2
		}
		r.EventRecorder.Eventf(cluster, corev1.EventTypeWarning, "MigrationTopologyChanged", "%s", err.Error())
		return false, err
	}
}

func (r *DragonflyClusterReconciler) applyMigrationResult(ctx context.Context, logger logr.Logger, cluster *dfv1alpha1.DragonflyCluster) error {
	m := cluster.Status.Migration
	if m == nil {
		return nil
	}

	updated := cluster.DeepCopy()

	// Ensure shards status exists and is indexed.
	byIndex := make(map[int32]*dfv1alpha1.DragonflyClusterShardStatus, len(updated.Status.Shards))
	for i := range updated.Status.Shards {
		ss := &updated.Status.Shards[i]
		byIndex[ss.ShardIndex] = ss
	}
	src := byIndex[m.SourceShard]
	dst := byIndex[m.TargetShard]
	if src == nil || dst == nil {
		return fmt.Errorf("missing shard status for migration src=%d dst=%d", m.SourceShard, m.TargetShard)
	}

	// Remove slots from src and add to dst in our status model.
	move := dfv1alpha1.DragonflyClusterSlotRange{Start: m.SlotStart, End: m.SlotEnd}
	src.SlotRanges = removeRange(src.SlotRanges, move)
	dst.SlotRanges = addRange(dst.SlotRanges, move)
	src.Slots = int32(slotRangesSize(src.SlotRanges))
	dst.Slots = int32(slotRangesSize(dst.SlotRanges))

	// Clear migration in status (caller will push config without migrations).
	updated.Status.Migration = nil
	setPhaseAndConditions(&updated.Status, dfcPhaseReconciling, updated.Generation, "MigrationFinished", "Migration finished, updating slot ownership")

	return r.Client.Status().Patch(ctx, updated, client.MergeFrom(cluster))
}

func (r *DragonflyClusterReconciler) patchClusterStatus(ctx context.Context, cluster *dfv1alpha1.DragonflyCluster, mutate func(st *dfv1alpha1.DragonflyClusterStatus)) error {
	patchFrom := client.MergeFrom(cluster.DeepCopy())
	mutate(&cluster.Status)
	return r.Client.Status().Patch(ctx, cluster, patchFrom)
}

func setPhaseAndConditions(st *dfv1alpha1.DragonflyClusterStatus, phase string, generation int64, reason string, message string) {
	st.Phase = phase
	st.ObservedGeneration = generation

	// Ready
	readyStatus := metav1.ConditionFalse
	readyReason := reason
	readyMessage := message
	if phase == dfcPhaseReady {
		readyStatus = metav1.ConditionTrue
		readyReason = "Ready"
		readyMessage = "Cluster is ready"
	}
	setStatusCondition(&st.Conditions, metav1.Condition{
		Type:               dfcCondReady,
		Status:             readyStatus,
		Reason:             defaultReason(readyReason, "Reconciling"),
		Message:            defaultMessage(readyMessage, readyReason, "Reconciling"),
		ObservedGeneration: generation,
	})

	// Rebalancing
	rebStatus := metav1.ConditionFalse
	rebReason := "NotRebalancing"
	rebMessage := "No slot migration in progress"
	if phase == dfcPhaseRebalancing || st.Migration != nil {
		rebStatus = metav1.ConditionTrue
		rebReason = "Rebalancing"
		if st.Migration != nil {
			rebMessage = fmt.Sprintf(
				"Migrating slots [%d,%d] from shard %d to shard %d (state=%s)",
				st.Migration.SlotStart, st.Migration.SlotEnd,
				st.Migration.SourceShard, st.Migration.TargetShard,
				st.Migration.State,
			)
		} else if message != "" {
			rebMessage = message
		}
	}
	setStatusCondition(&st.Conditions, metav1.Condition{
		Type:               dfcCondRebalancing,
		Status:             rebStatus,
		Reason:             defaultReason(rebReason, "NotRebalancing"),
		Message:            defaultMessage(rebMessage, rebReason, "NotRebalancing"),
		ObservedGeneration: generation,
	})

	// Degraded
	degStatus := metav1.ConditionFalse
	degReason := "Healthy"
	degMessage := "Cluster is healthy"
	if phase == dfcPhaseError {
		degStatus = metav1.ConditionTrue
		degReason = defaultReason(reason, "Error")
		degMessage = defaultMessage(message, reason, "Error")
	}
	setStatusCondition(&st.Conditions, metav1.Condition{
		Type:               dfcCondDegraded,
		Status:             degStatus,
		Reason:             defaultReason(degReason, "Healthy"),
		Message:            defaultMessage(degMessage, degReason, "Healthy"),
		ObservedGeneration: generation,
	})
}

func setStatusCondition(conds *[]metav1.Condition, cond metav1.Condition) {
	// Ensure reason/message are non-empty for nicer UX in kubectl.
	cond.Reason = defaultReason(cond.Reason, "Unknown")
	cond.Message = defaultMessage(cond.Message, cond.Reason, "Unknown")
	meta.SetStatusCondition(conds, cond)
}

func defaultReason(reason string, fallback string) string {
	if reason != "" {
		return reason
	}
	return fallback
}

func defaultMessage(message string, reason string, fallback string) string {
	if message != "" {
		return message
	}
	if reason != "" {
		return reason
	}
	return fallback
}

func removeArgsWithPrefix(args []string, prefix string) []string {
	if prefix == "" || len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			continue
		}
		out = append(out, a)
	}
	return out
}

func upsertEnvVar(env []corev1.EnvVar, v corev1.EnvVar) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == v.Name {
			env[i] = v
			return env
		}
	}
	return append(env, v)
}

func evenSlotRanges(shards int32) map[int32][]dfv1alpha1.DragonflyClusterSlotRange {
	const total = 16384
	res := make(map[int32][]dfv1alpha1.DragonflyClusterSlotRange, shards)
	if shards <= 0 {
		return res
	}
	per := total / int(shards)
	rem := total % int(shards)
	start := 0
	for i := int32(0); i < shards; i++ {
		size := per
		if i == shards-1 {
			size += rem
		}
		end := start + size - 1
		res[i] = []dfv1alpha1.DragonflyClusterSlotRange{{Start: int32(start), End: int32(end)}}
		start = end + 1
	}
	return res
}

func slotRangesSize(ranges []dfv1alpha1.DragonflyClusterSlotRange) int {
	total := 0
	for _, r := range ranges {
		total += int(r.End - r.Start + 1)
	}
	return total
}

func statusTotalSlots(shards []dfv1alpha1.DragonflyClusterShardStatus) int {
	total := 0
	for _, s := range shards {
		total += slotRangesSize(s.SlotRanges)
	}
	return total
}

func validateSlotCoverage(shards []dfv1alpha1.DragonflyClusterShardStatus, expectedShardCount int32) error {
	const totalSlots = 16384

	if expectedShardCount <= 0 {
		return fmt.Errorf("expectedShardCount must be > 0")
	}
	if len(shards) != int(expectedShardCount) {
		return fmt.Errorf("status shard count (%d) != spec shard count (%d)", len(shards), expectedShardCount)
	}

	seen := make([]bool, totalSlots)
	for _, s := range shards {
		for _, r := range s.SlotRanges {
			if r.Start < 0 || r.End < 0 || r.Start > 16383 || r.End > 16383 {
				return fmt.Errorf("slot range out of bounds for shard %d: [%d,%d]", s.ShardIndex, r.Start, r.End)
			}
			if r.Start > r.End {
				return fmt.Errorf("slot range start > end for shard %d: [%d,%d]", s.ShardIndex, r.Start, r.End)
			}
			for slot := r.Start; slot <= r.End; slot++ {
				i := int(slot)
				if seen[i] {
					return fmt.Errorf("duplicate slot %d detected (shard %d)", slot, s.ShardIndex)
				}
				seen[i] = true
			}
		}
	}

	for i, ok := range seen {
		if !ok {
			return fmt.Errorf("missing slot %d in status slot ranges", i)
		}
	}
	return nil
}

func normalizeRanges(ranges []dfv1alpha1.DragonflyClusterSlotRange) []dfv1alpha1.DragonflyClusterSlotRange {
	if len(ranges) == 0 {
		return ranges
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].Start == ranges[j].Start {
			return ranges[i].End < ranges[j].End
		}
		return ranges[i].Start < ranges[j].Start
	})
	out := []dfv1alpha1.DragonflyClusterSlotRange{ranges[0]}
	for _, r := range ranges[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End+1 {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

func removeRange(ranges []dfv1alpha1.DragonflyClusterSlotRange, rem dfv1alpha1.DragonflyClusterSlotRange) []dfv1alpha1.DragonflyClusterSlotRange {
	// Remove [rem.Start, rem.End] from ranges (assuming it is fully contained).
	var out []dfv1alpha1.DragonflyClusterSlotRange
	for _, r := range ranges {
		if rem.End < r.Start || rem.Start > r.End {
			out = append(out, r)
			continue
		}
		// overlap
		if rem.Start > r.Start {
			out = append(out, dfv1alpha1.DragonflyClusterSlotRange{Start: r.Start, End: rem.Start - 1})
		}
		if rem.End < r.End {
			out = append(out, dfv1alpha1.DragonflyClusterSlotRange{Start: rem.End + 1, End: r.End})
		}
	}
	return normalizeRanges(out)
}

func addRange(ranges []dfv1alpha1.DragonflyClusterSlotRange, add dfv1alpha1.DragonflyClusterSlotRange) []dfv1alpha1.DragonflyClusterSlotRange {
	return normalizeRanges(append(ranges, add))
}

func chooseRebalanceMigration(cluster *dfv1alpha1.DragonflyCluster, maxSlotsPerMigration int32) (dfv1alpha1.DragonflyClusterMigrationStatus, bool) {
	// Compute current slot count per shard from status.
	type shardSlots struct {
		Idx    int32
		Slots  int
		Ranges []dfv1alpha1.DragonflyClusterSlotRange
	}
	current := make([]shardSlots, 0, len(cluster.Status.Shards))
	for _, ss := range cluster.Status.Shards {
		current = append(current, shardSlots{
			Idx:    ss.ShardIndex,
			Slots:  slotRangesSize(ss.SlotRanges),
			Ranges: append([]dfv1alpha1.DragonflyClusterSlotRange{}, ss.SlotRanges...),
		})
	}
	if len(current) == 0 {
		return dfv1alpha1.DragonflyClusterMigrationStatus{}, false
	}

	num := int(cluster.Spec.Shards)
	if num <= 0 {
		return dfv1alpha1.DragonflyClusterMigrationStatus{}, false
	}

	// Desired distribution must match our initial allocation policy to avoid oscillation.
	// Reuse evenSlotRanges() so "remainder" assignment is consistent.
	desiredRanges := evenSlotRanges(cluster.Spec.Shards)
	desired := make(map[int32]int, num)
	for i := int32(0); i < cluster.Spec.Shards; i++ {
		desired[i] = slotRangesSize(desiredRanges[i])
	}

	// Identify sources and targets.
	var sources []shardSlots
	var targets []shardSlots
	for _, s := range current {
		want := desired[s.Idx]
		if s.Slots > want {
			sources = append(sources, s)
		} else if s.Slots < want {
			targets = append(targets, s)
		}
	}
	if len(sources) == 0 || len(targets) == 0 {
		return dfv1alpha1.DragonflyClusterMigrationStatus{}, false
	}

	// Prefer moving from most-overfull to most-underfull.
	sort.Slice(sources, func(i, j int) bool { return sources[i].Slots > sources[j].Slots })
	sort.Slice(targets, func(i, j int) bool { return targets[i].Slots < targets[j].Slots })

	src := sources[0]
	dst := targets[0]

	need := desired[dst.Idx] - dst.Slots
	excess := src.Slots - desired[src.Idx]
	moveSlots := minInt(need, excess)
	moveSlots = minInt(moveSlots, int(maxSlotsPerMigration))
	if moveSlots <= 0 {
		return dfv1alpha1.DragonflyClusterMigrationStatus{}, false
	}

	// Choose a contiguous range from the end of the largest range in src.
	ranges := normalizeRanges(src.Ranges)
	if len(ranges) == 0 {
		return dfv1alpha1.DragonflyClusterMigrationStatus{}, false
	}
	// Pick the last range (highest slots).
	r := ranges[len(ranges)-1]
	if int(r.End-r.Start+1) < moveSlots {
		moveSlots = int(r.End - r.Start + 1)
	}
	slotEnd := r.End
	slotStart := slotEnd - int32(moveSlots) + 1

	return dfv1alpha1.DragonflyClusterMigrationStatus{
		SourceShard: src.Idx,
		TargetShard: dst.Idx,
		SlotStart:   slotStart,
		SlotEnd:     slotEnd,
		State:       "STARTING",
		LastError:   "",
	}, true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func doWithRetry(ctx context.Context, attempts int, baseDelay time.Duration, op func(opCtx context.Context) error) error {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		opCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := op(opCtx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err

		// Exponential-ish backoff capped.
		sleep := baseDelay * time.Duration(i+1)
		if sleep > 2*time.Second {
			sleep = 2 * time.Second
		}

		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return lastErr
}
