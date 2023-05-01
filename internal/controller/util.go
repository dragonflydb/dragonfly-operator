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
	"time"

	"github.com/go-redis/redis"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	PhaseResourcesCreated string = "resources-created"

	PhaseResourcesUpdated string = "resources-updated"

	PhaseReady string = "ready"
)

// isPodOnLatestVersion returns if the Given pod is on the updatedRevision
// of the given statefulset or not
func isPodOnLatestVersion(ctx context.Context, c client.Client, pod *corev1.Pod, statefulSet *appsv1.StatefulSet) (bool, error) {
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

func waitForStatefulSetReady(ctx context.Context, c client.Client, name, namespace string, maxDuration time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for statefulset to be ready")
		default:
			// Check if the statefulset is ready
			ready, err := isStatefulSetReady(ctx, c, name, namespace)
			if err != nil {
				return err
			}
			if ready {
				return nil
			}
		}
	}
}

func isStatefulSetReady(ctx context.Context, c client.Client, name, namespace string) (bool, error) {
	var statefulSet appsv1.StatefulSet
	if err := c.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, &statefulSet); err != nil {
		return false, nil
	}

	if statefulSet.Status.ReadyReplicas == *statefulSet.Spec.Replicas {
		return true, nil
	}

	return false, nil
}

func checkForStableState(ctx context.Context, c client.Client, pod *corev1.Pod) error {
	// wait until pod IP is ready
	if pod.Status.PodIP == "" || pod.Status.Phase != corev1.PodRunning {
		return nil
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%d", pod.Status.PodIP, 6379),
	})

	_, err := redisClient.Ping().Result()
	if err != nil {
		return err
	}

	info, err := redisClient.Info("replication").Result()
	if err != nil {
		return err
	}

	if info == "" {
		return errors.New("empty info")
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
		return fmt.Errorf("master_sync_in_progress:%s", data["master_sync_in_progress"])
	}

	if data["master_link_status"] != "up" {
		return fmt.Errorf("master_link_status:%s", data["master_link_status"])
	}

	if data["master_last_io_seconds_ago"] == "-1" {
		return fmt.Errorf("master_last_io_seconds_ago:%s", data["master_last_io_seconds_ago"])
	}

	return nil
}
