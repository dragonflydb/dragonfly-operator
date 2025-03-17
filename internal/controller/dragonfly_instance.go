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
	"net"
	"strconv"
	"strings"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/redis/go-redis/v9"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (dfi *DragonflyInstance) configureReplication(ctx context.Context) error {
	dfi.log.Info("Configuring replication")

	pods, err := dfi.getPods(ctx)
	if err != nil {
		return err
	}

	// remove master pod label if it exists
	// This is important as the pod termination could take a while in
	// the deleted case causing unnecessary master reconcilation as 2 masters
	// could exist at the same time.
	for _, pod := range pods.Items {
		if pod.Labels[resources.Role] == resources.Master {
			delete(pod.Labels, resources.Role)
			if err := dfi.client.Update(ctx, &pod); err != nil {
				return err
			}
		}
	}

	var master string
	var masterIp string
	for _, pod := range pods.Items {
		if isPodReady(pod) {
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

	if master == "" {
		dfi.log.Info("Couldn't find a healthy pod to configure as master")
		return errors.New("couldn't find a healthy pod to configure as master")
	}

	// Mark others as replicas
	markedPods := 0
	for _, pod := range pods.Items {
		// only mark the running non-master pods
		dfi.log.Info("Checking pod", "podName", pod.Name, "ip", pod.Status.PodIP, "status", pod.Status.Phase, "deletiontimestamp", pod.DeletionTimestamp)
		if pod.Name != master && isPodReady(pod) {
			dfi.log.Info("Marking pod as replica", "podName", pod.Name, "ip", pod.Status.PodIP, "status", pod.Status.Phase)
			if err := dfi.replicaOf(ctx, &pod, masterIp); err != nil {
				// TODO: Why does this fail every now and then?
				// Should replication be continued if it fails?
				dfi.log.Error(err, "Failed to mark pod as replica", "podName", pod.Name)
				return err
			} else {
				markedPods++
			}
		}
	}

	dfi.log.Info(fmt.Sprintf("Successfully marked %d/%d replicas", markedPods, len(pods.Items)-1))
	if err := dfi.updateStatus(ctx, PhaseReady); err != nil {
		return err
	}

	return nil
}

func (dfi *DragonflyInstance) updateStatus(ctx context.Context, phase string) error {
	// get latest df object first
	if err := dfi.client.Get(ctx, types.NamespacedName{
		Name:      dfi.df.Name,
		Namespace: dfi.df.Namespace,
	}, dfi.df); err != nil {
		return err
	}

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
		if isPodReady(pod) && pod.Labels[resources.Role] == resources.Master {
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
		if isPodReady(pod) && pod.Labels[resources.Role] == resources.Master {
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

// checkReplicaRole checks if the given pod is a replica and if it is
// connected to the right master
func (dfi *DragonflyInstance) checkReplicaRole(ctx context.Context, pod *corev1.Pod, masterIp string) (bool, error) {
	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%d", pod.Status.PodIP, resources.DragonflyAdminPort),
	})
	defer redisClient.Close()

	resp, err := redisClient.Info(ctx, "replication").Result()
	if err != nil {
		return false, err
	}

	var redisRole string
	for _, line := range strings.Split(resp, "\n") {
		if strings.Contains(line, "role") {
			redisRole = strings.Trim(strings.Split(line, ":")[1], "\r")
		}
	}

	if redisRole != resources.Replica {
		return false, nil
	}

	var redisMasterIp string
	// check if it is connected to the right master
	for _, line := range strings.Split(resp, "\n") {
		if strings.Contains(line, "master_host") {
			redisMasterIp = strings.Trim(strings.Split(line, ":")[1], "\r")
		}
	}

	if masterIp != redisMasterIp && masterIp != pod.Labels[resources.MasterIp] {
		return false, nil
	}

	return true, nil
}

// checkAndConfigureReplication checks if all the pods are assigned to
// the correct role and if not, configures them accordingly
func (dfi *DragonflyInstance) checkAndConfigureReplication(ctx context.Context) error {
	dfi.log.Info("checking if all pods are configured correctly")
	pods, err := dfi.getPods(ctx)
	if err != nil {
		return err
	}

	// retry if there are pods that are not running
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			dfi.log.Info("not all pods are running. retrying", "pod", pod.Name)
			return nil
		}
	}

	// check for one master and all replicas
	podRoles := make(map[string][]string)
	for _, pod := range pods.Items {
		podRoles[pod.Labels[resources.Role]] = append(podRoles[pod.Labels[resources.Role]], pod.Name)
	}

	if len(podRoles[resources.Master]) != 1 {
		dfi.log.Info("incorrect number of masters. reconfiguring replication", "masters", podRoles[resources.Master])
		if err = dfi.configureReplication(ctx); err != nil {
			return err
		}
	}

	if len(podRoles[resources.Replica]) != len(pods.Items)-1 {
		dfi.log.Info("incorrect number of replicas", "replicas", podRoles[resources.Replica])

		// configure non replica pods as replicas
		for _, pod := range pods.Items {
			if pod.Labels[resources.Role] == "" {
				if isPodReady(pod) {
					if err := dfi.configureReplica(ctx, &pod); err != nil {
						return err
					}
				}
			}
		}
	}

	masterIp, err := dfi.getMasterIp(ctx)
	if err != nil {
		return err
	}

	for _, pod := range pods.Items {
		if pod.Labels[resources.Role] == resources.Replica {
			ok, err := dfi.checkReplicaRole(ctx, &pod, masterIp)
			if err != nil {
				return err
			}

			// configuring to the right master
			if !ok {
				dfi.log.Info("configuring pod as replica to the right master", "pod", pod.Name)
				if err := dfi.configureReplica(ctx, &pod); err != nil {
					return err
				}
			}
		}
	}

	dfi.log.Info("all pods are configured correctly", "dfi", dfi.df.Name)
	return nil
}

func (dfi *DragonflyInstance) getPods(ctx context.Context) (*corev1.PodList, error) {
	dfi.log.Info("getting all pods relevant to the instance")
	var pods corev1.PodList
	if err := dfi.client.List(ctx, &pods, client.InNamespace(dfi.df.Namespace), client.MatchingLabels{
		resources.DragonflyNameLabelKey:    dfi.df.Name,
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
	redisClient := redis.NewClient(&redis.Options{
		Addr: net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
	})
	defer redisClient.Close()

	dfi.log.Info("Trying to invoke SLAVE OF command", "pod", pod.Name, "master", masterIp, "addr", redisClient.Options().Addr)
	resp, err := redisClient.SlaveOf(ctx, masterIp, fmt.Sprint(resources.DragonflyAdminPort)).Result()
	if err != nil {
		return fmt.Errorf("error running SLAVE OF command: %s", err)
	}

	if resp != "OK" {
		return fmt.Errorf("response of `SLAVE OF` on replica is not OK: %s", resp)
	}

	dfi.log.Info("Marking pod role as replica", "pod", pod.Name)
	pod.Labels[resources.Role] = resources.Replica
	pod.Labels[resources.MasterIp] = masterIp
	if err := dfi.client.Update(ctx, pod); err != nil {
		return fmt.Errorf("could not update replica label")
	}

	return nil
}

// replicaOfNoOne configures the pod as a master
// along while updating other pods to be replicas
func (dfi *DragonflyInstance) replicaOfNoOne(ctx context.Context, pod *corev1.Pod) error {
	redisClient := redis.NewClient(&redis.Options{
		Addr: net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
	})
	defer redisClient.Close()

	dfi.log.Info("Running SLAVE OF NO ONE command", "pod", pod.Name, "addr", redisClient.Options().Addr)
	resp, err := redisClient.SlaveOf(ctx, "NO", "ONE").Result()
	if err != nil {
		return fmt.Errorf("error running SLAVE OF NO ONE command: %w", err)
	}

	if resp != "OK" {
		return fmt.Errorf("response of `SLAVE OF NO ONE` on master is not OK: %s", resp)
	}

	dfi.log.Info("Marking pod role as master", "pod", pod.Name)
	pod.Labels[resources.Role] = resources.Master
	if err := dfi.client.Update(ctx, pod); err != nil {
		return err
	}

	return nil
}
