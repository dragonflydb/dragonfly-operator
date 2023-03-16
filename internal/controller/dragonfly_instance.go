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

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/go-redis/redis"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DragonFlyPod denotes the specific pod part of a Dragonfly instance
// TODO: Add logging
type DragonflyInstance struct {
	// DragonFly is the instance that pod is part of
	df *resourcesv1.Dragonfly

	client client.Client
}

func GetDragonFlyInstanceFromPod(ctx context.Context, c client.Client, pod *corev1.Pod) (*DragonflyInstance, error) {
	dfName, ok := pod.Labels["app"]
	if !ok {
		return nil, errors.New("can't find the `app` label")
	}

	// Retrieve the relevant Dragonfly object
	var df dfv1alpha1.Dragonfly
	err := c.Get(ctx, types.NamespacedName{
		Name:      dfName,
		Namespace: pod.Namespace,
	}, &df)
	if err != nil {
		return nil, err
	}

	return &DragonflyInstance{
		df:     &df,
		client: c,
	}, nil
}

func (d *DragonflyInstance) initReplication(ctx context.Context) error {
	if err := d.updateStatus(ctx, PhaseMarking); err != nil {
		return err
	}

	pods, err := d.getPods(ctx)
	if err != nil {
		return err
	}

	var master string
	var masterIp string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.ContainerStatuses[0].Ready && pod.Labels[resources.Role] != resources.Master {
			master = pod.Name
			masterIp = pod.Status.PodIP
			if err := d.configureAsMaster(ctx, &pod); err != nil {
				return err
			}
			break
		}
	}

	// Mark others as replicas
	for _, pod := range pods.Items {
		if pod.Name != master {
			if err := d.configureAsReplica(ctx, &pod, masterIp); err != nil {
				return err
			}
		}
	}

	if err := d.updateStatus(ctx, PhaseReady); err != nil {
		return err
	}

	return nil
}

func (d *DragonflyInstance) updateStatus(ctx context.Context, phase string) error {
	d.df.Status.Phase = phase
	if err := d.client.Status().Update(ctx, d.df); err != nil {
		return err
	}

	return nil
}

func (d *DragonflyInstance) masterExists(ctx context.Context) (bool, error) {
	pods, err := d.getPods(ctx)
	if err != nil {
		return false, err
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.ContainerStatuses[0].Ready && pod.Labels[resources.Role] == resources.Master {
			return true, nil
		}
	}

	return false, nil
}

func (d *DragonflyInstance) getMasterIp(ctx context.Context) (string, error) {
	pods, err := d.getPods(ctx)
	if err != nil {
		return "", err
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.ContainerStatuses[0].Ready && pod.Labels[resources.Role] == resources.Master {
			return pod.Status.PodIP, nil
		}
	}

	return "", errors.New("could not find master")
}

func (d *DragonflyInstance) addReplica(ctx context.Context, pod *corev1.Pod) error {
	masterIp, err := d.getMasterIp(ctx)
	if err != nil {
		return err
	}

	if err := d.configureAsReplica(ctx, pod, masterIp); err != nil {
		return err
	}

	if err := d.updateStatus(ctx, PhaseReady); err != nil {
		return err
	}

	return nil
}

// configureAsReplica configures the pod as a replica
// to an existing master
func (d *DragonflyInstance) configureAsReplica(ctx context.Context, pod *corev1.Pod, masterIp string) error {
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

	pod.Labels[resources.Role] = resources.Replica
	if err := d.client.Update(ctx, pod); err != nil {
		return fmt.Errorf("could not update replica label")
	}

	return nil
}

// configureAsMaster configures the pod as a master
// along while updating other pods to be replicas
func (d *DragonflyInstance) configureAsMaster(ctx context.Context, pod *corev1.Pod) error {
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

	pod.Labels[resources.Role] = resources.Master
	if err := d.client.Update(ctx, pod); err != nil {
		return err
	}

	return nil
}

// Given a  pod, marks it as a master and update all other pods to be replicas
func (d *DragonflyInstance) updateMaster(ctx context.Context, newMaster *corev1.Pod) error {
	if err := d.updateStatus(ctx, PhaseMarking); err != nil {
		return err
	}

	if err := d.configureAsMaster(ctx, newMaster); err != nil {
		return err
	}

	pods, err := d.getPods(ctx)
	if err != nil {
		return err
	}

	// Mark others as replicas
	for _, pod := range pods.Items {
		if pod.Name != newMaster.Name {
			if err := d.configureAsReplica(ctx, &pod, newMaster.Status.PodIP); err != nil {
				return err
			}
		}
	}

	if err := d.updateStatus(ctx, PhaseReady); err != nil {
		return err
	}

	return nil
}

func (d *DragonflyInstance) getPods(ctx context.Context) (*corev1.PodList, error) {
	var pods corev1.PodList
	if err := d.client.List(ctx, &pods, client.InNamespace(d.df.Namespace), client.MatchingLabels{
		"app":                              d.df.Name,
		resources.KubernetesPartOfLabelKey: "dragonfly",
	},
	); err != nil {
		return nil, err
	}

	return &pods, nil
}
