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
	"errors"
	"fmt"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

const (
	PhaseResourcesCreated string = "ResourcesCreated"
	PhaseReady            string = "Ready"
	PhaseRollingUpdate    string = "RollingUpdate"
	PhaseConfiguring      string = "Configuring"
	// PhaseReadyOld TODO: remove this in a future release.
	PhaseReadyOld string = "ready"
)

var (
	ErrNoMaster         = errors.New("no master found")
	ErrNoHealthyMaster  = errors.New("no healthy master found")
	ErrIncorrectMasters = errors.New("incorrect number of masters")
)

// isPodOnLatestVersion returns true if the given pod is on the updated revision of the given StatefulSet.
func isPodOnLatestVersion(pod *corev1.Pod, updateRevision string) bool {
	if podRevision, ok := pod.Labels[appsv1.StatefulSetRevisionLabel]; ok && podRevision == updateRevision {
		return true
	}

	return false
}

// getUpdatedReplica returns a replica pod that is on the latest version of the given StatefulSet.
func getUpdatedReplica(replicas *corev1.PodList, updateRevision string) (*corev1.Pod, error) {
	// Iterate over the replicas and find a replica which is on the latest version
	for _, replica := range replicas.Items {
		if isPodOnLatestVersion(&replica, updateRevision) {
			return &replica, nil
		}
	}

	return nil, fmt.Errorf("no replica pod found on latest version")
}

// roleExists returns true if the pod has a role label.
func roleExists(pod *corev1.Pod) bool {
	_, ok := pod.Labels[resources.RoleLabelKey]
	return ok
}

// isRunningAndReady returns true if the pod is running and ready
func isHealthy(pod *corev1.Pod) bool {
	return isRunningAndReady(pod) && !isTerminating(pod)
}

// isRunningAndReady checks if the pod is running and ready
func isRunningAndReady(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodRunning && isReady(pod)
}

// isReady returns true if the pod and the dragonfly container are ready.
func isReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue && pod.Status.PodIP != "" {
			return isDragonflyContainerReady(pod.Status.ContainerStatuses)
		}
	}

	return false
}

// isDragonflyContainerReady returns true if the dragonfly container is ready.
func isDragonflyContainerReady(containerStatuses []corev1.ContainerStatus) bool {
	for _, cs := range containerStatuses {
		if cs.Name == resources.DragonflyContainerName {
			return cs.Ready
		}
	}

	return false
}

// isTerminating returns true if the pod is terminating.
func isTerminating(pod *corev1.Pod) bool {
	if !pod.DeletionTimestamp.IsZero() {
		return true
	}

	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.DisruptionTarget && c.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

// getGVK returns the GroupVersionKind of the given object.
func getGVK(obj client.Object, scheme *runtime.Scheme) schema.GroupVersionKind {
	gvk, err := apiutil.GVKForObject(obj, scheme)
	if err != nil {
		return schema.GroupVersionKind{Group: "Unknown", Version: "Unknown", Kind: "Unknown"}
	}
	return gvk
}

// needRollingUpdate returns true if the given pods require a rolling update.
func needRollingUpdate(pods *corev1.PodList, sts *appsv1.StatefulSet) bool {
	if sts.Status.UpdatedReplicas != sts.Status.Replicas {
		for _, pod := range pods.Items {
			if !isPodOnLatestVersion(&pod, sts.Status.UpdateRevision) {
				return true
			}
		}
	}

	return false
}

// getDragonflyName returns the dragonfly name from the pod labels
func getDragonflyName(pod *corev1.Pod) (string, error) {
	if name, ok := pod.Labels[resources.DragonflyNameLabelKey]; ok {
		return name, nil
	}

	return "", fmt.Errorf("can't find the `%s` label", resources.DragonflyNameLabelKey)
}

// isMaster returns true if the pod is a master
func isMaster(pod *corev1.Pod) bool {
	if role, ok := pod.Labels[resources.RoleLabelKey]; ok && role == resources.Master {
		return true
	}

	return false
}

// isReplica returns true if the pod is a replica
func isReplica(pod *corev1.Pod) bool {
	if role, ok := pod.Labels[resources.RoleLabelKey]; ok && role == resources.Replica {
		return true
	}

	return false
}

// isMasterError returns true if the error is related to the master.
func isMasterError(err error) bool {
	return errors.Is(err, ErrNoMaster) ||
		errors.Is(err, ErrNoHealthyMaster) ||
		errors.Is(err, ErrIncorrectMasters)
}
