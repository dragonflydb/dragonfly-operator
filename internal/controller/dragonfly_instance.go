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

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DragonflyInstance is an abstraction over the `Dragonfly` CRD
// and provides methods to handle replication.
type DragonflyInstance struct {
	// Dragonfly is the relevant Dragonfly CRD that it performs actions over
	df *resourcesv1.Dragonfly

	client client.Client
	marker replicationMarker
	log    logr.Logger
}

func GetDragonflyInstanceFromPod(ctx context.Context, c client.Client, pod *corev1.Pod, marker replicationMarker, log logr.Logger) (*DragonflyInstance, error) {
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

	// Use InClusterConfigurer by default
	if marker == nil {
		marker = NewInclusterConfigurer(c)
	}

	return &DragonflyInstance{
		df:     &df,
		client: c,
		marker: marker,
		log:    log,
	}, nil
}

func (d *DragonflyInstance) initReplication(ctx context.Context) error {
	d.log.Info("Initializing replication")
	if err := d.updateStatus(ctx, PhaseConfiguringReplication); err != nil {
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
			if err := d.marker.replicaOfNoOne(ctx, &pod); err != nil {
				return err
			}
			break
		}
	}

	// Mark others as replicas
	for _, pod := range pods.Items {
		if pod.Name != master {
			if err := d.marker.replicaOf(ctx, &pod, masterIp); err != nil {
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
	d.log.Info("Updating status", "phase", phase)
	d.df.Status.Phase = phase
	if err := d.client.Status().Update(ctx, d.df); err != nil {
		return err
	}

	return nil
}

func (d *DragonflyInstance) masterExists(ctx context.Context) (bool, error) {
	d.log.Info("checking if a master exists already")
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
	d.log.Info("retrieving ip of the master")
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

// configureReplica marks the given pod as a replica by finding
// a master for that instance
func (d *DragonflyInstance) configureReplica(ctx context.Context, pod *corev1.Pod) error {
	d.log.Info("configuring pod as replica", "pod", pod.Name)
	masterIp, err := d.getMasterIp(ctx)
	if err != nil {
		return err
	}

	if err := d.marker.replicaOf(ctx, pod, masterIp); err != nil {
		return err
	}

	if err := d.updateStatus(ctx, PhaseReady); err != nil {
		return err
	}

	return nil
}

// configureMaster marks the given pod as a master while also marking
// every other pod as replica
func (d *DragonflyInstance) configureMaster(ctx context.Context, newMaster *corev1.Pod) error {
	d.log.Info("configuring pod as master", "pod", newMaster.Name)
	if err := d.updateStatus(ctx, PhaseConfiguringReplication); err != nil {
		return err
	}

	if err := d.marker.replicaOfNoOne(ctx, newMaster); err != nil {
		return err
	}

	pods, err := d.getPods(ctx)
	if err != nil {
		return err
	}

	d.log.Info("configuring other pods as replicas")
	// Mark others as replicas
	for _, pod := range pods.Items {
		if pod.Name != newMaster.Name {
			if err := d.marker.replicaOf(ctx, &pod, newMaster.Status.PodIP); err != nil {
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
	d.log.Info("getting all pods relevant to the instance")
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
