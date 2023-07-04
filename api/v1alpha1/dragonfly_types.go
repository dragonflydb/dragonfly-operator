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

	// (Optional) Annotations to add to the Dragonfly pods.
	// +optional
	// +kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// (Optional) Dragonfly container resource limits. Any container limits
	// can be specified.
	// +optional
	// +kubebuilder:validation:Optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// (Optional) Dragonfly pod affinity
	// +optional
	// +kubebuilder:validation:Optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// (Optional) Dragonfly pod tolerations
	// +optional
	// +kubebuilder:validation:Optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
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
