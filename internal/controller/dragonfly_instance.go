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
	"github.com/go-logr/logr"
	"github.com/go-redis/redis"
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
	log    logr.Logger
}

func GetDragonflyInstanceFromPod(ctx context.Context, c client.Client, pod *corev1.Pod, log logr.Logger) (*DragonflyInstance, error) {
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
		log:    log,
	}, nil
}

func (dfi *DragonflyInstance) getStatus(ctx context.Context) (string, error) {
	if err := dfi.client.Get(ctx, types.NamespacedName{
		Name:      dfi.df.Name,
		Namespace: dfi.df.Namespace,
	}, dfi.df); err != nil {
		return "", err
	}

	return dfi.df.Status.Phase, nil
}

func (dfi *DragonflyInstance) configureReplication(ctx context.Context) error {
	dfi.log.Info("Configuring replication")
	if err := dfi.updateStatus(ctx, PhaseConfiguringReplication); err != nil {
		return err
	}

	pods, err := dfi.getPods(ctx)
	if err != nil {
		return err
	}

	var master string
	var masterIp string
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning && pod.Status.ContainerStatuses[0].Ready && pod.Labels[resources.Role] != resources.Master && pod.DeletionTimestamp == nil {
			master = pod.Name
			masterIp = pod.Status.PodIP
			dfi.log.Info("Marking pod as master", "podName", master, "ip", masterIp)
			if err := dfi.replicaOfNoOne(ctx, &pod); err != nil {
				dfi.log.Error(err, "Failed to mark pod as master", "podName", pod.Name)
				return err
			}
			break
		}
	}

	// Mark others as replicas
	for _, pod := range pods.Items {
		// only mark the running non-master pods
		dfi.log.Info("Checking pod", "podName", pod.Name, "ip", pod.Status.PodIP, "status", pod.Status.Phase, "deletiontimestamp", pod.DeletionTimestamp)
		if pod.Name != master && pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
			dfi.log.Info("Marking pod as replica", "podName", pod.Name, "ip", pod.Status.PodIP, "status", pod.Status.Phase)
			if err := dfi.replicaOf(ctx, &pod, masterIp); err != nil {
				// TODO: Why does this fail every now and then?
				// Should replication be continued if it fails?
				dfi.log.Error(err, "Failed to mark pod as replica", "podName", pod.Name)
				return err
			}
		}
	}

	if err := dfi.updateStatus(ctx, PhaseReady); err != nil {
		return err
	}

	return nil
}

func (dfi *DragonflyInstance) updateStatus(ctx context.Context, phase string) error {
	dfi.log.Info("Updating status", "phase", phase)
	dfi.df.Status.Phase = phase
	if err := dfi.client.Status().Update(ctx, dfi.df); err != nil {
		return err
	}

	return nil
}

func (dfi *DragonflyInstance) masterExists(ctx context.Context) (bool, error) {
	dfi.log.Info("checking if a master exists already")
	pods, err := dfi.getPods(ctx)
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

func (dfi *DragonflyInstance) getMasterIp(ctx context.Context) (string, error) {
	dfi.log.Info("retrieving ip of the master")
	pods, err := dfi.getPods(ctx)
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
func (dfi *DragonflyInstance) configureReplica(ctx context.Context, pod *corev1.Pod) error {
	dfi.log.Info("configuring pod as replica", "pod", pod.Name)
	masterIp, err := dfi.getMasterIp(ctx)
	if err != nil {
		return err
	}

	if err := dfi.replicaOf(ctx, pod, masterIp); err != nil {
		return err
	}

	if err := dfi.updateStatus(ctx, PhaseReady); err != nil {
		return err
	}

	return nil
}

// configureMaster marks the given pod as a master while also marking
// every other pod as replica
func (dfi *DragonflyInstance) configureMaster(ctx context.Context, newMaster *corev1.Pod) error {
	dfi.log.Info("configuring pod as master", "pod", newMaster.Name)
	if err := dfi.updateStatus(ctx, PhaseConfiguringReplication); err != nil {
		return err
	}

	if err := dfi.replicaOfNoOne(ctx, newMaster); err != nil {
		return err
	}

	pods, err := dfi.getPods(ctx)
	if err != nil {
		return err
	}

	dfi.log.Info("configuring other pods as replicas")
	// Mark others as replicas
	for _, pod := range pods.Items {
		if pod.Name != newMaster.Name {
			if err := dfi.replicaOf(ctx, &pod, newMaster.Status.PodIP); err != nil {
				return err
			}
		}
	}

	if err := dfi.updateStatus(ctx, PhaseReady); err != nil {
		return err
	}

	return nil
}

func (dfi *DragonflyInstance) getPods(ctx context.Context) (*corev1.PodList, error) {
	dfi.log.Info("getting all pods relevant to the instance")
	var pods corev1.PodList
	if err := dfi.client.List(ctx, &pods, client.InNamespace(dfi.df.Namespace), client.MatchingLabels{
		"app":                              dfi.df.Name,
		resources.KubernetesPartOfLabelKey: "dragonfly",
	},
	); err != nil {
		return nil, err
	}

	return &pods, nil
}

// replicaOf configures the pod as a replica
// to the given master instance
func (dfi *DragonflyInstance) replicaOf(ctx context.Context, pod *corev1.Pod, masterIp string) error {
	// retry logic as port-forwarding is not reliable in CI
	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:6379", pod.Status.PodIP),
	})

	dfi.log.Info("Trying to invoke SLAVE OF command", "pod", pod.Name, "master", masterIp)
	resp, err := redisClient.SlaveOf(masterIp, "6379").Result()
	if err != nil {
		return fmt.Errorf("Error running SLAVE OF command: %s", err)
	}

	if resp != "OK" {
		return fmt.Errorf("Response of `SLAVE OF` on replica is not OK: %s", resp)
	}

	dfi.log.Info("Marking pod role as replica", "pod", pod.Name)
	pod.Labels[resources.Role] = resources.Replica
	if err := dfi.client.Update(ctx, pod); err != nil {
		return fmt.Errorf("could not update replica label")
	}

	return nil
}

// replicaOfNoOne configures the pod as a master
// along while updating other pods to be replicas
func (dfi *DragonflyInstance) replicaOfNoOne(ctx context.Context, pod *corev1.Pod) error {
	// retry logic as command issuance can timeout and fail
	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:6379", pod.Status.PodIP),
	})

	dfi.log.Info("Running SLAVE OF NO ONE command", "pod", pod.Name)
	resp, err := redisClient.SlaveOf("NO", "ONE").Result()
	if err != nil {
		return fmt.Errorf("Error running SLAVE OF NO ONE command: %w", err)
	}

	if resp != "OK" {
		return fmt.Errorf("Response of `SLAVE OF NO NE` on master is not OK: %s", resp)
	}

	dfi.log.Info("Marking pod role as master", "pod", pod.Name)
	pod.Labels[resources.Role] = resources.Master
	if err := dfi.client.Update(ctx, pod); err != nil {
		return err
	}

	return nil
}
