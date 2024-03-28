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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// DragonflySpec defines the desired state of Dragonfly
type DragonflySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Replicas is the total number of Dragonfly instances including the master
	Replicas int32 `json:"replicas,omitempty"`

	// Image is the Dragonfly image to use
	Image string `json:"image,omitempty"`

	// (Optional) Dragonfly container args to pass to the container
	// Refer to the Dragonfly documentation for the list of supported args
	// +optional
	// +kubebuilder:validation:Optional
	Args []string `json:"args,omitempty"`

	// (Optional) Acl file Secret to pass to the container
	// +optional
	// +kubebuilder:validation:Optional
	AclFromSecret *corev1.SecretKeySelector `json:"aclFromSecret,omitempty"`

	// (Optional) Annotations to add to the Dragonfly pods.
	// +optional
	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// (Optional) Labels to add to the Dragonfly pods.
	// +optional
	// +kubebuilder:validation:Optional
	Labels map[string]string `json:"labels,omitempty"`

	// (Optional) Env variables to add to the Dragonfly pods.
	// +optional
	// +kubebuilder:validation:Optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// (Optional) Dragonfly container resource limits. Any container limits
	// can be specified.
	// +optional
	// +kubebuilder:validation:Optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// (Optional) Dragonfly pod affinity
	// +optional
	// +kubebuilder:validation:Optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// (Optional) Dragonfly pod node selector
	// +optional
	// +kubebuilder:validation:Optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// (Optional) Dragonfly memcached port
	// +optional
	// +kubebuilder:validation:Optional
	MemcachedPort int32 `json:"memcachedPort,omitempty"`

	// (Optional) Dragonfly pod tolerations
	// +optional
	// +kubebuilder:validation:Optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// (Optional) Dragonfly pod topologySpreadConstraints
	// +optional
	// +kubebuilder:validation:Optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`

	// (Optional) Dragonfly Authentication mechanism
	// +optional
	// +kubebuilder:validation:Optional
	Authentication *Authentication `json:"authentication,omitempty"`

	// (Optional) Dragonfly pod service account name
	// +optional
	// +kubebuilder:validation:Optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// (Optional) Dragonfly pod priority class name
	// +optional
	// +kubebuilder:validation:Optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// (Optional) Dragonfly TLS secret to used for TLS
	// Connections to Dragonfly. Dragonfly instance  must
	// have access to this secret and be in the same namespace
	// +optional
	// +kubebuilder:validation:Optional
	TLSSecretRef *corev1.SecretReference `json:"tlsSecretRef,omitempty"`

	// (Optional) Dragonfly Snapshot configuration
	// +optional
	// +kubebuilder:validation:Optional
	Snapshot *Snapshot `json:"snapshot,omitempty"`

	// (Optional) Skip Assigning FileSystem Group. Required for platforms such as Openshift that require IDs to not be set, as it injects a fixed randomized ID per namespace into all pods.
	// +optional
	// +kubebuilder:validation:Optional
	SkipFSGroup bool `json:"skipFSGroup,omitempty"`

	// (Optional) Dragonfly Service configuration
	// +optional
	// +kubebuilder:validation:Optional
	ServiceSpec *ServiceSpec `json:"serviceSpec,omitempty"`
}

type ServiceSpec struct {
	// (Optional) Dragonfly Service type
	// +optional
	// +kubebuilder:validation:Optional
	Type corev1.ServiceType `json:"type,omitempty"`

	// (Optional) Dragonfly Service Annotations
	// +optional
	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

type Snapshot struct {
	// (Optional) The path to the snapshot directory
	// This can also be an S3 URI with the prefix `s3://` when
	// using S3 as the snapshot backend
	// +optional
	// +kubebuilder:validation:Optional
	Dir string `json:"dir,omitempty"`

	// (Optional) Dragonfly snapshot schedule
	// +optional
	// +kubebuilder:validation:Optional
	Cron string `json:"cron,omitempty"`

	// (Optional) Dragonfly PVC spec
	// +optional
	// +kubebuilder:validation:Optional
	PersistentVolumeClaimSpec *corev1.PersistentVolumeClaimSpec `json:"persistentVolumeClaimSpec,omitempty"`
}

type Authentication struct {
	// (Optional) Dragonfly Password from Secret as a reference to a specific key
	// +optional
	PasswordFromSecret *corev1.SecretKeySelector `json:"passwordFromSecret,omitempty"`

	// (Optional) If specified, the Dragonfly instance will check if the
	// client certificate is signed by this CA. Server TLS must be enabled for this.
	// +optional
	ClientCaCertSecret *corev1.SecretKeySelector `json:"clientCaCertSecret,omitempty"`
}

// DragonflyStatus defines the observed state of Dragonfly
type DragonflyStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Status of the Dragonfly Instance
	// It can be one of the following:
	// - "ready": The Dragonfly instance is ready to serve requests
	// - "configuring-replication": The controller is updating the master of the Dragonfly instance
	// - "resources-created": The Dragonfly instance resources were created but not yet configured
	Phase string `json:"phase,omitempty"`

	// IsRollingUpdate is true if the Dragonfly instance is being updated
	IsRollingUpdate bool `json:"isRollingUpdate,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Dragonfly is the Schema for the dragonflies API
type Dragonfly struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DragonflySpec   `json:"spec,omitempty"`
	Status DragonflyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// DragonflyList contains a list of Dragonfly
type DragonflyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Dragonfly `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Dragonfly{}, &DragonflyList{})
}
