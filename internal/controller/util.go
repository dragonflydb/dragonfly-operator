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
	"strings"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/redis/go-redis/v9"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	PhaseResourcesCreated string = "resources-created"

	PhaseReady string = "ready"
)

// isPodOnLatestVersion returns if the Given pod is on the updatedRevision
// of the given statefulset or not
func isPodOnLatestVersion(pod *corev1.Pod, statefulSet *appsv1.StatefulSet) (bool, error) {
	// Get the pod's revision
	podRevision, ok := pod.Labels[appsv1.StatefulSetRevisionLabel]
	if !ok {
		return false, fmt.Errorf("pod %s/%s does not have a revision label", pod.Namespace, pod.Name)
	}

	// Compare the two
	if podRevision == statefulSet.Status.UpdateRevision {
		return true, nil
	}

	return false, nil
}

// getLatestReplica returns a replica pod which is on the latest version
// of the given statefulset
func getLatestReplica(ctx context.Context, c client.Client, statefulSet *appsv1.StatefulSet) (*corev1.Pod, error) {
	// Get the list of pods
	podList := &corev1.PodList{}
	err := c.List(ctx, podList, &client.ListOptions{
		Namespace: statefulSet.Namespace,
		LabelSelector: labels.SelectorFromValidatedSet(map[string]string{
			resources.DragonflyNameLabelKey:    statefulSet.Name,
			resources.KubernetesPartOfLabelKey: "dragonfly",
		}),
	})
	if err != nil {
		return nil, err
	}

	// Iterate over the pods and find a replica which is on the latest version
	for _, pod := range podList.Items {

		isLatest, err := isPodOnLatestVersion(&pod, statefulSet)
		if err != nil {
			return nil, err
		}

		if isLatest && pod.Labels[resources.Role] == resources.Replica {
			return &pod, nil
		}
	}

	return nil, errors.New("no replica pod found on latest version")

}

// replTakeover runs the replTakeOver on the given replica pod
func replTakeover(ctx context.Context, c client.Client, newMaster *corev1.Pod) error {
	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%d", newMaster.Status.PodIP, resources.DragonflyAdminPort),
	})
	defer redisClient.Close()

	resp, err := redisClient.Do(ctx, "repltakeover", "10000").Result()
	if err != nil {
		return fmt.Errorf("error running REPLTAKEOVER command: %w", err)
	}

	if resp != "OK" {
		return fmt.Errorf("response of `REPLTAKEOVER` on replica is not OK: %s", resp)
	}

	// update the label on the pod
	newMaster.Labels[resources.Role] = resources.Master
	if err := c.Update(ctx, newMaster); err != nil {
		return fmt.Errorf("error updating the role label on the pod: %w", err)
	}
	return nil
}

func isStableState(ctx context.Context, pod *corev1.Pod) (bool, error) {
	// wait until pod IP is ready
	if pod.Status.PodIP == "" || pod.Status.Phase != corev1.PodRunning {
		return false, nil
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%d", pod.Status.PodIP, resources.DragonflyAdminPort),
	})
	defer redisClient.Close()

	_, err := redisClient.Ping(ctx).Result()
	if err != nil {
		return false, err
	}

	info, err := redisClient.Info(ctx, "replication").Result()
	if err != nil {
		return false, err
	}

	if info == "" {
		return false, errors.New("empty info")
	}

	data := map[string]string{}
	for _, line := range strings.Split(info, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kv := strings.Split(line, ":")
		data[kv[0]] = strings.TrimSuffix(kv[1], "\r")
	}

	if data["master_sync_in_progress"] == "1" {
		return false, nil
	}

	if data["master_link_status"] != "up" {
		return false, nil
	}

	if data["master_last_io_seconds_ago"] == "-1" {
		return false, nil
	}

	return true, nil
}

func isPodReady(pod corev1.Pod) bool {
	if isPodMarkedForDeletion(pod) {
		return false
	}

	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue && pod.Status.PodIP != "" {
			return isDragonflyContainerReady(pod.Status.ContainerStatuses)
		}
	}

	return false
}

func isDragonflyContainerReady(containerStatuses []corev1.ContainerStatus) bool {
	for _, cs := range containerStatuses {
		if cs.Name == resources.DragonflyContainerName && cs.Ready {
			return true
		}
	}

	return false
}

func isPodMarkedForDeletion(pod corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if !pod.DeletionTimestamp.IsZero() || (c.Type == corev1.DisruptionTarget && c.Status == corev1.ConditionTrue) {
			return true
		}
	}
	return false
}
