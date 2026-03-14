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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DragonflyClusterSpec defines the desired state of a multi-shard Dragonfly cluster-mode deployment.
//
// Important: Dragonfly multi-shard cluster-mode intentionally ships without a control-plane.
// This CRD is the control-plane: it provisions shards (masters) with replicas and orchestrates
// slot allocation and migrations via `DFLYCLUSTER CONFIG` + `DFLYCLUSTER SLOT-MIGRATION-STATUS`.
type DragonflyClusterSpec struct {
	// Shards is the desired number of primary/master shards.
	// +kubebuilder:validation:Minimum=1
	Shards int32 `json:"shards"`

	// ReplicasPerShard is the desired number of replicas per shard (excluding the master).
	// Total pods per shard will be (1 + replicasPerShard).
	// +kubebuilder:validation:Minimum=0
	ReplicasPerShard int32 `json:"replicasPerShard,omitempty"`

	// Template defines common settings applied to each shard (image, resources, args, auth, snapshots, etc).
	// The operator will override/augment fields that are required for cluster-mode correctness
	// (e.g. `--cluster_mode=yes`, stable cluster node IDs).
	//
	// Note: `template.replicas` is ignored; shard pod count is controlled by `replicasPerShard`.
	Template DragonflySpec `json:"template,omitempty"`

	// Rebalance controls cluster rebalance behavior when scaling out (and optionally continuously).
	// +optional
	Rebalance *DragonflyClusterRebalanceSpec `json:"rebalance,omitempty"`
}

type DragonflyClusterRebalanceSpec struct {
	// Enabled controls whether the operator should automatically rebalance slots when the shard count changes.
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled,omitempty"`

	// MaxSlotsPerMigration limits the size of a single migration range.
	// Smaller values reduce risk/impact but increase time to rebalance.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default:=1024
	MaxSlotsPerMigration int32 `json:"maxSlotsPerMigration,omitempty"`

	// MaxConcurrentMigrations limits concurrent migrations per cluster.
	// NOTE: the current implementation performs at most one migration at a time.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default:=1
	MaxConcurrentMigrations int32 `json:"maxConcurrentMigrations,omitempty"`
}

type DragonflyClusterSlotRange struct {
	// Start is the first slot in the range (inclusive).
	Start int32 `json:"start"`
	// End is the last slot in the range (inclusive).
	End int32 `json:"end"`
}

type DragonflyClusterShardStatus struct {
	// ShardIndex is the shard ordinal.
	ShardIndex int32 `json:"shardIndex,omitempty"`
	// DragonflyName is the underlying per-shard Dragonfly CR name.
	DragonflyName string `json:"dragonflyName,omitempty"`
	// MasterPodName is the current master pod name for the shard.
	MasterPodName string `json:"masterPodName,omitempty"`
	// Slots is the number of slots currently owned by this shard's master (as tracked by the operator).
	Slots int32 `json:"slots,omitempty"`
	// SlotRanges is the list of slot ranges currently owned by this shard (as tracked by the operator).
	SlotRanges []DragonflyClusterSlotRange `json:"slotRanges,omitempty"`
}

type DragonflyClusterMigrationStatus struct {
	// SourceShard is the shard index we are migrating slots from.
	SourceShard int32 `json:"sourceShard,omitempty"`
	// TargetShard is the shard index we are migrating slots to.
	TargetShard int32 `json:"targetShard,omitempty"`
	// SourceNodeID is the node id of the source shard master when the migration was started.
	// +optional
	SourceNodeID string `json:"sourceNodeId,omitempty"`
	// TargetNodeID is the node id of the target shard master when the migration was started.
	// +optional
	TargetNodeID string `json:"targetNodeId,omitempty"`
	// TargetIP is the target master Pod IP when the migration was started (used for slot migration transport).
	// +optional
	TargetIP string `json:"targetIP,omitempty"`
	// SlotStart is the start slot (inclusive).
	SlotStart int32 `json:"slotStart,omitempty"`
	// SlotEnd is the end slot (inclusive).
	SlotEnd int32 `json:"slotEnd,omitempty"`
	// State reflects the last observed migration state from SLOT-MIGRATION-STATUS.
	State string `json:"state,omitempty"`
	// LastError is the last observed error string (if any).
	LastError string `json:"lastError,omitempty"`
	// StartedAt is when the operator initiated this migration.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`
}

// DragonflyClusterStatus defines the observed state of DragonflyCluster.
type DragonflyClusterStatus struct {
	Phase              string `json:"phase,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the DragonflyCluster's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	Shards []DragonflyClusterShardStatus `json:"shards,omitempty"`

	// Migration is non-nil when a slot migration is currently in progress.
	// +optional
	Migration *DragonflyClusterMigrationStatus `json:"migration,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The current phase of the DragonflyCluster"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status",description="Whether the cluster is Ready"
//+kubebuilder:printcolumn:name="Shards",type="integer",JSONPath=".spec.shards",description="Desired shard count"
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`,description="Age"

// DragonflyCluster is the Schema for the dragonflyclusters API.
type DragonflyCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DragonflyClusterSpec   `json:"spec,omitempty"`
	Status DragonflyClusterStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DragonflyClusterList contains a list of DragonflyCluster.
type DragonflyClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DragonflyCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DragonflyCluster{}, &DragonflyClusterList{})
}
