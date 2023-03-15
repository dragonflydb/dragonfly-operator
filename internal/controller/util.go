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

	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/go-redis/redis"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	PhaseInitialized string = "initialized"

	PhaseMarking string = "marking"

	PhaseReady string = "ready"
)

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

func activeMasterExists(ctx context.Context, c client.Client, df *resourcesv1.Dragonfly) (bool, error) {
	log := log.FromContext(ctx)
	log.Info(fmt.Sprintf("Checking if active master exists for %s", df.Name))

	pods := corev1.PodList{}
	if err := c.List(ctx, &pods, client.InNamespace(df.Namespace), client.MatchingLabels{
		"app":                              df.Name,
		resources.KubernetesPartOfLabelKey: "dragonfly",
	},
	); err != nil {
		log.Error(err, "could not list Pods")
		return false, err
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.ContainerStatuses[0].Ready && pod.Labels[resources.Role] == resources.Master {
			return true, nil
		}
	}

	return false, nil
}

func configureReplication(ctx context.Context, c client.Client, df *resourcesv1.Dragonfly) error {
	log := log.FromContext(ctx)
	log.Info(fmt.Sprintf("Finding healthy and marking active for %s", df.Name))

	pods := corev1.PodList{}
	if err := c.List(ctx, &pods, client.InNamespace(df.Namespace), client.MatchingLabels{
		"app":                              df.Name,
		resources.KubernetesPartOfLabelKey: "dragonfly",
	},
	); err != nil {
		log.Error(err, "could not list Pods")
		return err
	}

	var master string
	var masterIp string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.ContainerStatuses[0].Ready && pod.Labels["role"] != "master" {
			master = pod.Name
			masterIp = pod.Status.PodIP
			if err := configureAsMaster(ctx, c, pod); err != nil {
				return err
			}
			break
		}
	}

	// Mark others as replicas
	for _, pod := range pods.Items {
		if pod.Name != master {
			if err := configureAsReplica(ctx, c, pod, masterIp); err != nil {
				return err
			}
		}
	}

	return nil
}

func configureReplicaFromDF(ctx context.Context, c client.Client, replicaPod corev1.Pod, db *resourcesv1.Dragonfly) error {
	log := log.FromContext(ctx)
	log.Info(fmt.Sprintf("Marking replica from db for %s", db.Name))

	pods := corev1.PodList{}

	if err := c.List(ctx, &pods, client.InNamespace(db.Namespace), client.MatchingLabels{
		"app":                              db.Name,
		resources.KubernetesPartOfLabelKey: "dragonfly",
	},
	); err != nil {
		log.Error(err, "could not list Pods")
		return err
	}

	var masterIp string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.ContainerStatuses[0].Ready && pod.Labels[resources.Role] == resources.Master {
			masterIp = pod.Status.PodIP
			break
		}
	}

	if err := configureAsReplica(ctx, c, replicaPod, masterIp); err != nil {
		log.Error(err, "could not mark replica")
		return err
	}

	return nil
}

func configureAsReplica(ctx context.Context, client client.Client, pod corev1.Pod, masterIp string) error {
	log := log.FromContext(ctx)
	log.Info(fmt.Sprintf("Marking %s as replica", pod.Name))

	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:6379", pod.Status.PodIP),
	})

	resp, err := redisClient.SlaveOf(masterIp, "6379").Result()
	if err != nil {
		return err
	}

	if resp != "OK" {
		return fmt.Errorf("could not mark instance as active")
	}

	pod.Labels["role"] = "replica"
	if err := client.Update(ctx, &pod); err != nil {
		return fmt.Errorf("could not update replica label")
	}

	return nil
}

func configureAsMaster(ctx context.Context, client client.Client, pod corev1.Pod) error {
	log := log.FromContext(ctx)
	log.Info(fmt.Sprintf("Marking %s as active", pod.Name))

	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:6379", pod.Status.PodIP),
	})

	resp, err := redisClient.SlaveOf("NO", "ONE").Result()
	if err != nil {
		return err
	}

	if resp != "OK" {
		return fmt.Errorf("could not mark instance as master")
	}

	pod.Labels["role"] = "master"
	if err := client.Update(ctx, &pod); err != nil {
		log.Error(err, "could not update Pod")
		return err
	}

	return nil
}
