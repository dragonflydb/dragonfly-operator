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
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/go-logr/logr"
	"github.com/redis/go-redis/v9"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// DragonflyInstance is an abstraction over the `Dragonfly` CRD and provides methods to handle replication.
type DragonflyInstance struct {
	// Dragonfly is the relevant Dragonfly CRD that it performs actions over
	df *dfv1alpha1.Dragonfly

	client        client.Client
	log           logr.Logger
	scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

// configureReplication configures the given pod as a master and other pods as replicas
func (dfi *DragonflyInstance) configureReplication(ctx context.Context, master *corev1.Pod) error {
	dfi.log.Info("configuring replication")

	pods, err := dfi.getPods(ctx)
	if err != nil {
		dfi.log.Error(err, "failed to get dragonfly pods")
		return err
	}

	dfiStatus := dfi.getStatus()
	dfiStatus.Phase = PhaseConfiguring
	if err = dfi.patchStatus(ctx, dfiStatus); err != nil {
		dfi.log.Error(err, "failed to update the dragonfly status")
		return err
	}

	dfi.log.Info("configuring pod as master", "pod", master.Name, "ip", master.Status.PodIP)
	if err = dfi.replicaOfNoOne(ctx, master); err != nil {
		dfi.log.Error(err, "failed to configure master", "pod", master.Name)
		return err
	}

	dfiStatus.Phase = PhaseReady
	if err = dfi.patchStatus(ctx, dfiStatus); err != nil {
		dfi.log.Error(err, "failed to update the dragonfly status")
		return err
	}

	dfi.eventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Replication", "Updated master instance")

	for _, pod := range pods.Items {
		if pod.Name == master.Name {
			continue
		}

		ready, readyErr := dfi.isPodReady(ctx, &pod)
		if readyErr != nil {
			dfi.log.Error(readyErr, "failed to check pod readiness", "pod", pod.Name)
			return readyErr
		}

		if ready {
			if err = dfi.configureReplica(ctx, &pod, master.Status.PodIP); err != nil {
				dfi.log.Error(err, "failed to configure replica", "pod", pod.Name)
				return err
			}
		}
	}

	return nil
}

// configureReplica configures the given pod as a replica to the given master
func (dfi *DragonflyInstance) configureReplica(ctx context.Context, pod *corev1.Pod, masterIp string) error {
	dfi.log.Info("configuring pod as replica", "pod", pod.Name, "ip", pod.Status.PodIP)

	if err := dfi.replicaOf(ctx, pod, masterIp); err != nil {
		return err
	}

	return nil
}

// checkReplicaRole returns true if the given pod is a replica and is connected to the correct master.
func (dfi *DragonflyInstance) checkReplicaRole(ctx context.Context, pod *corev1.Pod, masterIp string) (bool, error) {
	redisClient := redis.NewClient(&redis.Options{
		Addr: net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
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

	if redisRole == resources.Master {
		return false, nil
	}

	var redisMasterIp string
	// check if it is connected to the right master
	for _, line := range strings.Split(resp, "\n") {
		if strings.Contains(line, "master_host") {
			redisMasterIp = strings.Trim(strings.Split(line, ":")[1], "\r")
		}
	}

	if masterIp != redisMasterIp && masterIp != pod.Labels[resources.MasterIpLabelKey] {
		return false, nil
	}

	return true, nil
}

// isReplicaStable returns true if the given replica is stable.
func (dfi *DragonflyInstance) isReplicaStable(ctx context.Context, pod *corev1.Pod) (bool, error) {
	redisClient := redis.NewClient(&redis.Options{
		Addr: net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
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
		return false, fmt.Errorf("empty info")
	}

	replicationData := parseInfoToMap(info)

	if val, ok := replicationData["master_sync_in_progress"]; ok && val == "1" {
		return false, nil
	}

	if val, ok := replicationData["master_link_status"]; ok && val != "up" {
		return false, nil
	}

	if val, ok := replicationData["master_last_io_seconds_ago"]; ok && val == "-1" {
		return false, nil
	}

	persistenceInfo, err := redisClient.Info(ctx, "persistence").Result()
	if err != nil {
		return false, err
	}

	persistenceData := parseInfoToMap(persistenceInfo)

	if val, ok := persistenceData["loading"]; ok && val != "0" && val != "" {
		return false, nil
	}

	if val, ok := persistenceData["load_state"]; ok && val != "" && val != "done" {
		return false, nil
	}

	return true, nil
}

// checkAndConfigureReplicas checks whether all replicas are configured correctly and if not configures them to the right master
func (dfi *DragonflyInstance) checkAndConfigureReplicas(ctx context.Context, masterIp string) error {
	pods, err := dfi.getPods(ctx)
	if err != nil {
		dfi.log.Error(err, "failed to get dragonfly pods")
		return err
	}

	for _, pod := range pods.Items {
		if isReplica(&pod) {
			dfi.log.Info("checking if replica is configured correctly", "pod", pod.Name)
			ok, err := dfi.checkReplicaRole(ctx, &pod, masterIp)
			if err != nil {
				return err
			}
			// configuring to the right master
			if !ok {
				dfi.log.Info("configuring pod as replica to the right master", "pod", pod.Name)
				if err := dfi.configureReplica(ctx, &pod, masterIp); err != nil {
					return err
				}
			}
		}

		if !roleExists(&pod) {
			ready, readyErr := dfi.isPodReady(ctx, &pod)
			if readyErr != nil {
				dfi.log.Error(readyErr, "failed to check pod readiness", "pod", pod.Name)
				return readyErr
			}

			if ready {
				if err := dfi.configureReplica(ctx, &pod, masterIp); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// getStatefulSet gets the statefulset object for the dragonfly instance
func (dfi *DragonflyInstance) getStatefulSet(ctx context.Context) (*appsv1.StatefulSet, error) {
	dfi.log.Info("getting statefulset")
	var sts appsv1.StatefulSet
	if err := dfi.client.Get(ctx, client.ObjectKey{Namespace: dfi.df.Namespace, Name: dfi.df.Name}, &sts); err != nil {
		return nil, err
	}
	return &sts, nil
}

// getPods gets all the pods relevant to the dragonfly instance
func (dfi *DragonflyInstance) getPods(ctx context.Context) (*corev1.PodList, error) {
	dfi.log.Info("getting all pods relevant to the dragonfly instance")
	var pods corev1.PodList
	if err := dfi.client.List(ctx, &pods, client.InNamespace(dfi.df.Namespace), client.MatchingLabels{
		resources.DragonflyNameLabelKey:     dfi.df.Name,
		resources.KubernetesPartOfLabelKey:  resources.KubernetesPartOf,
		resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
	}); err != nil {
		return nil, err
	}

	return &pods, nil
}

// getMaster gets the master pod for the dragonfly instance
func (dfi *DragonflyInstance) getMaster(ctx context.Context) (*corev1.Pod, error) {
	var masterPods corev1.PodList

	matchingLabels := client.MatchingLabels{
		resources.DragonflyNameLabelKey:     dfi.df.Name,
		resources.KubernetesPartOfLabelKey:  resources.KubernetesPartOf,
		resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
		resources.RoleLabelKey:              resources.Master,
	}

	if dfi.getStatus().Phase == PhaseRollingUpdate || dfi.getStatus().IsRollingUpdate {
		statefulSet, err := dfi.getStatefulSet(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get statefulset: %w", err)
		}

		matchingLabels[appsv1.StatefulSetRevisionLabel] = statefulSet.Status.UpdateRevision

		if err = dfi.client.List(ctx, &masterPods, client.InNamespace(dfi.df.Namespace), matchingLabels); err != nil {
			return nil, fmt.Errorf("failed to list pods: %w", err)
		}
	}

	if dfi.getStatus().Phase == PhaseReady || dfi.getStatus().Phase == PhaseReadyOld || len(masterPods.Items) == 0 {
		delete(matchingLabels, appsv1.StatefulSetRevisionLabel)
		if err := dfi.client.List(ctx, &masterPods, client.InNamespace(dfi.df.Namespace), matchingLabels); err != nil {
			return nil, fmt.Errorf("failed to list pods: %w", err)
		}
	}

	if len(masterPods.Items) == 0 {
		return nil, ErrNoMaster
	}

	var healthyMasters []corev1.Pod
	for _, pod := range masterPods.Items {
		ready, readyErr := dfi.isPodReady(ctx, &pod)
		if readyErr != nil {
			return nil, fmt.Errorf("failed to verify master readiness: %w", readyErr)
		}
		if ready {
			healthyMasters = append(healthyMasters, pod)
		}
	}

	if len(healthyMasters) == 0 {
		return nil, ErrNoHealthyMaster
	}

	if len(healthyMasters) > 1 {
		return nil, ErrIncorrectMasters
	}

	return &healthyMasters[0], nil
}

// getReplicas gets all the replicas for the dragonfly instance
func (dfi *DragonflyInstance) getReplicas(ctx context.Context) (*corev1.PodList, error) {
	var replicas corev1.PodList
	if err := dfi.client.List(ctx, &replicas, client.InNamespace(dfi.df.Namespace), client.MatchingLabels{
		resources.DragonflyNameLabelKey:     dfi.df.Name,
		resources.KubernetesPartOfLabelKey:  resources.KubernetesPartOf,
		resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
		resources.RoleLabelKey:              resources.Replica,
	}); err != nil {
		return nil, err
	}

	return &replicas, nil
}

// getHealthyPod gets the first healthy pod for the dragonfly instance
func (dfi *DragonflyInstance) getHealthyPod(ctx context.Context) (*corev1.Pod, error) {
	pods, err := dfi.getPods(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dragonfly pods: %w", err)
	}

	for _, pod := range pods.Items {
		ready, readyErr := dfi.isPodReady(ctx, &pod)
		if readyErr != nil {
			return nil, fmt.Errorf("failed to verify pod readiness: %w", readyErr)
		}
		if ready {
			return &pod, nil
		}
	}

	return nil, fmt.Errorf("no healthy pod found")
}

// getStatus gets the status of the dragonfly instance
func (dfi *DragonflyInstance) getStatus() dfv1alpha1.DragonflyStatus {
	return dfi.df.Status
}

// patchStatus patches the status of the dragonfly instance
func (dfi *DragonflyInstance) patchStatus(ctx context.Context, status dfv1alpha1.DragonflyStatus) error {
	dfi.log.Info("updating status", "status", status)

	patchFrom := client.MergeFrom(dfi.df.DeepCopy())
	dfi.df.Status = status
	if err := dfi.client.Status().Patch(ctx, dfi.df, patchFrom); err != nil {
		return err
	}

	return nil
}

// detectOldMasters checks whether there are any old masters and deletes them
func (dfi *DragonflyInstance) detectOldMasters(ctx context.Context, updateRevision string) error {
	var masterPods corev1.PodList

	if err := dfi.client.List(ctx, &masterPods, client.InNamespace(dfi.df.Namespace), client.MatchingLabels{
		resources.DragonflyNameLabelKey:     dfi.df.Name,
		resources.KubernetesPartOfLabelKey:  resources.KubernetesPartOf,
		resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
		resources.RoleLabelKey:              resources.Master,
	}); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	for _, pod := range masterPods.Items {
		if !isPodOnLatestVersion(&pod, updateRevision) {
			dfi.log.Info("deleting old master pod", "pod", pod.Name, "pod_revision", pod.Labels[appsv1.StatefulSetRevisionLabel], "update_revision", updateRevision)
			if err := dfi.client.Delete(ctx, &pod); err != nil {
				return fmt.Errorf("failed to delete pod: %w", err)
			}
		}
	}

	return nil
}

// replicaOf configures the pod as a replica to the given master instance
func (dfi *DragonflyInstance) replicaOf(ctx context.Context, pod *corev1.Pod, masterIp string) error {
	redisClient := redis.NewClient(&redis.Options{
		Addr: net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
	})
	defer redisClient.Close()

	dfi.log.Info("trying to invoke SLAVE OF command", "pod", pod.Name, "master", masterIp, "addr", redisClient.Options().Addr)
	resp, err := redisClient.SlaveOf(ctx, masterIp, fmt.Sprint(resources.DragonflyAdminPort)).Result()
	if err != nil {
		return fmt.Errorf("error running SLAVE OF command: %s", err)
	}

	if resp != "OK" {
		return fmt.Errorf("response of `SLAVE OF` on replica is not OK: %s", resp)
	}

	if dfi.df.Spec.Snapshot != nil && dfi.df.Spec.Snapshot.EnableOnMasterOnly {
		dfi.log.Info("clearing snapshot cron schedule on replica", "pod", pod.Name)
		if _, err := redisClient.ConfigSet(ctx, "snapshot_cron", "").Result(); err != nil {
			return fmt.Errorf("failed to clear snapshot_cron on replica %s: %w", pod.Name, err)
		}
	}

	dfi.log.Info("marking pod role as replica", "pod", pod.Name)
	patchFrom := client.MergeFrom(pod.DeepCopy())
	pod.Labels[resources.RoleLabelKey] = resources.Replica
	pod.Labels[resources.MasterIpLabelKey] = masterIp
	if err := dfi.client.Patch(ctx, pod, patchFrom); err != nil {
		return fmt.Errorf("failed to update the role label on the pod: %w", err)
	}

	return nil
}

// replicaOfNoOne configures the pod as a master along while updating other pods to be replicas
func (dfi *DragonflyInstance) replicaOfNoOne(ctx context.Context, pod *corev1.Pod) error {
	redisClient := redis.NewClient(&redis.Options{
		Addr: net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
	})
	defer redisClient.Close()

	dfi.log.Info("running SLAVE OF NO ONE command", "pod", pod.Name, "addr", redisClient.Options().Addr)
	resp, err := redisClient.SlaveOf(ctx, "NO", "ONE").Result()
	if err != nil {
		return fmt.Errorf("error running SLAVE OF NO ONE command: %w", err)
	}

	if resp != "OK" {
		return fmt.Errorf("response of `SLAVE OF NO ONE` on master is not OK: %s", resp)
	}

	if dfi.df.Spec.Snapshot != nil && dfi.df.Spec.Snapshot.EnableOnMasterOnly {
		dfi.log.Info("setting snapshot cron schedule on master", "pod", pod.Name)
		cron := dfi.df.Spec.Snapshot.Cron
		if _, err := redisClient.ConfigSet(ctx, "snapshot_cron", cron).Result(); err != nil {
			return fmt.Errorf("failed to set snapshot_cron on master %s: %w", pod.Name, err)
		}
	}

	dfi.log.Info("marking pod role as master", "pod", pod.Name)
	patchFrom := client.MergeFrom(pod.DeepCopy())
	pod.Labels[resources.RoleLabelKey] = resources.Master
	delete(pod.Labels, resources.MasterIpLabelKey)
	if err := dfi.client.Patch(ctx, pod, patchFrom); err != nil {
		return fmt.Errorf("failed to update the role label on the pod: %w", err)
	}

	return nil
}

// reconcileResources creates or updates the dragonfly resources
func (dfi *DragonflyInstance) reconcileResources(ctx context.Context) error {
	dfResources, err := resources.GenerateDragonflyResources(dfi.df)
	if err != nil {
		return fmt.Errorf("failed to generate dragonfly resources")
	}
	for _, desired := range dfResources {
		dfi.log.Info("reconciling dragonfly resource", "kind", getGVK(desired, dfi.scheme).Kind, "namespace", desired.GetNamespace(), "Name", desired.GetName())

		existing := desired.DeepCopyObject().(client.Object)
		err = dfi.client.Get(ctx, client.ObjectKey{
			Namespace: desired.GetNamespace(),
			Name:      desired.GetName(),
		}, existing)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to get resource: %w", err)
			}
			// Resource does not exist, create it
			if err := controllerutil.SetControllerReference(dfi.df, desired, dfi.scheme); err != nil {
				return fmt.Errorf("failed to set controller reference: %w", err)
			}
			err = dfi.client.Create(ctx, desired)
			if err != nil {
				return fmt.Errorf("failed to create resource: %w", err)
			}
			dfi.log.Info("created resource", "resource", desired.GetName())
			continue
		}
		// Resource exists, prepare desired for potential update
		if err := controllerutil.SetControllerReference(dfi.df, desired, dfi.scheme); err != nil {
			return fmt.Errorf("failed to set controller reference: %w", err)
		}
		// Special handling for Services to preserve immutable fields
		if svcDesired, ok := desired.(*corev1.Service); ok {
			if svcExisting, ok := existing.(*corev1.Service); ok {
				svcDesired.Spec.ClusterIP = svcExisting.Spec.ClusterIP
				svcDesired.Spec.IPFamilies = svcExisting.Spec.IPFamilies
				svcDesired.Spec.IPFamilyPolicy = svcExisting.Spec.IPFamilyPolicy
				// Preserve NodePorts for NodePort and LoadBalancer services
				if svcDesired.Spec.Type == corev1.ServiceTypeNodePort || svcDesired.Spec.Type == corev1.ServiceTypeLoadBalancer {
					for i := range svcDesired.Spec.Ports {
						for j := range svcExisting.Spec.Ports {
							if svcDesired.Spec.Ports[i].Name == svcExisting.Spec.Ports[j].Name {
								svcDesired.Spec.Ports[i].NodePort = svcExisting.Spec.Ports[j].NodePort
								break
							}
						}
					}
				}
				// Also preserve HealthCheckNodePort if external
				if svcDesired.Spec.Type == corev1.ServiceTypeLoadBalancer && svcDesired.Spec.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyLocal {
					svcDesired.Spec.HealthCheckNodePort = svcExisting.Spec.HealthCheckNodePort
				}
			}
		}
		// Compare specs; skip if no changes
		if resourceSpecsEqual(desired, existing) {
			dfi.log.Info("no changes detected, skipping update", "resource", desired.GetName())
			continue
		}
		// Update if specs differ
		desired.SetResourceVersion(existing.GetResourceVersion())
		if err = dfi.client.Update(ctx, desired); err != nil {
			return fmt.Errorf("failed to update resource: %w", err)
		}
		dfi.log.Info("updated resource", "resource", desired.GetName())
	}
	if dfi.df.Spec.Replicas < 2 {
		if err = dfi.client.Delete(ctx, &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Name:      dfi.df.Name,
				Namespace: dfi.df.Namespace,
			},
		}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete pod disruption budget: %w", err)
		}
	}
	status := dfi.getStatus()
	if status.Phase == "" {
		status.Phase = PhaseResourcesCreated
		if err = dfi.patchStatus(ctx, status); err != nil {
			return fmt.Errorf("failed to update the dragonfly object")
		}
		dfi.eventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Resources", "Created resources")
	}
	return nil
}

// Helper function to compare resource specs (add to the file)
func resourceSpecsEqual(desired, existing client.Object) bool {
	// Compare metadata labels and annotations
	if !reflect.DeepEqual(desired.GetLabels(), existing.GetLabels()) || !reflect.DeepEqual(desired.GetAnnotations(), existing.GetAnnotations()) {
		return false
	}
	// Compare only the .Spec field using reflection
	desiredV := reflect.ValueOf(desired).Elem()
	existingV := reflect.ValueOf(existing).Elem()
	desiredSpec := desiredV.FieldByName("Spec")
	existingSpec := existingV.FieldByName("Spec")
	if !desiredSpec.IsValid() || !existingSpec.IsValid() {
		return true // No spec field, consider equal
	}
	return reflect.DeepEqual(desiredSpec.Interface(), existingSpec.Interface())
}

func parseInfoToMap(info string) map[string]string {
	data := make(map[string]string)
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(strings.TrimSuffix(parts[1], "\r"))
		data[key] = value
	}
	return data
}

func (dfi *DragonflyInstance) isDatasetLoaded(ctx context.Context, pod *corev1.Pod) (bool, error) {
	if pod.Status.PodIP == "" {
		return false, nil
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
	})
	defer redisClient.Close()

	persistenceInfo, err := redisClient.Info(ctx, "persistence").Result()
	if err != nil {
		return false, err
	}

	data := parseInfoToMap(persistenceInfo)

	if val, ok := data["loading"]; ok && val != "" && val != "0" {
		return false, nil
	}

	if val, ok := data["load_state"]; ok && val != "" && val != "done" {
		return false, nil
	}

	return true, nil
}

func (dfi *DragonflyInstance) isPodReady(ctx context.Context, pod *corev1.Pod) (bool, error) {
	if !isRunningAndReady(pod) || isTerminating(pod) {
		return false, nil
	}

	loaded, err := dfi.isDatasetLoaded(ctx, pod)
	if err != nil {
		return false, fmt.Errorf("failed to determine dataset load status: %w", err)
	}

	return loaded, nil
}

// detectRollingUpdate checks whether the pod spec has changed and performs a rolling update if needed
func (dfi *DragonflyInstance) detectRollingUpdate(ctx context.Context) (dfv1alpha1.DragonflyStatus, error) {
	dfi.log.Info("checking if pod spec has changed")
	status := dfi.getStatus()
	statefulSet, err := dfi.getStatefulSet(ctx)
	if err != nil {
		return status, fmt.Errorf("failed to get statefulset: %w", err)
	}

	pods, err := dfi.getPods(ctx)
	if err != nil {
		return status, fmt.Errorf("failed to get dragonfly pods: %w", err)
	}

	if needRollingUpdate(pods, statefulSet) {
		dfi.log.Info("pod spec has changed, performing a rollout")
		dfi.eventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Rollout", "Starting a rollout")

		status.Phase = PhaseRollingUpdate
		if err = dfi.patchStatus(ctx, status); err != nil {
			return status, fmt.Errorf("failed to update the dragonfly status: %w", err)
		}
		dfi.eventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Resources", "Performing a rollout")
	}
	return status, nil
}

// deleteMasterRoleLabel deletes the role label from the pods
func (dfi *DragonflyInstance) deleteMasterRoleLabel(ctx context.Context) error {
	pods, err := dfi.getPods(ctx)
	if err != nil {
		return fmt.Errorf("failed to get dragonfly pods: %w", err)
	}

	for _, pod := range pods.Items {
		if isMaster(&pod) {
			if err = dfi.deleteRoleLabel(ctx, &pod); err != nil {
				return err
			}
		}
	}

	return nil
}

// deleteRoleLabel deletes the role label from the given pod
func (dfi *DragonflyInstance) deleteRoleLabel(ctx context.Context, pod *corev1.Pod) error {
	dfi.log.Info("deleting pod role label", "pod", pod.Name, "role", pod.Labels[resources.RoleLabelKey])

	patchFrom := client.MergeFrom(pod.DeepCopy())
	delete(pod.Labels, resources.RoleLabelKey)

	if err := dfi.client.Patch(ctx, pod, patchFrom); err != nil {
		dfi.log.Error(err, "failed to update the role label", "pod", pod.Name)
		return err
	}

	return nil
}

// allPodsHealthy checks whether all pods are healthy, and deletes pods that are outdated and failed to start
func (dfi *DragonflyInstance) allPodsHealthy(ctx context.Context, updateRevision string) (ctrl.Result, error) {
	pods, err := dfi.getPods(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get dragonfly pods: %w", err)
	}

	for _, pod := range pods.Items {
		if !isPodOnLatestVersion(&pod, updateRevision) && !isTerminating(&pod) && !isRunningAndReady(&pod) {
			dfi.log.Info("deleting failed to start pod", "pod", pod.Name)
			if err := dfi.client.Delete(ctx, &pod); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to delete pod: %w", err)
			}

			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		ready, readyErr := dfi.isPodReady(ctx, &pod)
		if readyErr != nil {
			return ctrl.Result{}, fmt.Errorf("failed to verify pod readiness: %w", readyErr)
		}

		if !ready {
			dfi.log.Info("waiting for pod to finish startup", "pod", pod.Name)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	return ctrl.Result{}, nil
}

// verifyUpdatedReplicas checks whether the updated replicas are in a stable state.
func (dfi *DragonflyInstance) verifyUpdatedReplicas(ctx context.Context, replicas *corev1.PodList, updateRevision string) (ctrl.Result, error) {
	for _, replica := range replicas.Items {
		if isPodOnLatestVersion(&replica, updateRevision) {
			dfi.log.Info("new replica found. checking if replica had a full sync", "pod", replica.Name)

			ok, err := dfi.isReplicaStable(ctx, &replica)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to check if replica is stable: %w", err)
			}

			if !ok {
				dfi.log.Info("not all new replicas are in stable status yet", "pod", replica.Name, "reason", err)
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			dfi.log.Info("replica is in stable state", "pod", replica.Name)
		}
	}

	return ctrl.Result{}, nil
}

// updatedReplicas updates the replicas to the latest version
func (dfi *DragonflyInstance) updatedReplicas(ctx context.Context, replicas *corev1.PodList, updateRevision string) (ctrl.Result, error) {
	for _, replica := range replicas.Items {
		if !isPodOnLatestVersion(&replica, updateRevision) {
			dfi.log.Info("deleting replica", "pod", replica.Name)
			dfi.eventRecorder.Event(dfi.df, corev1.EventTypeNormal, "Rollout", "Deleting replica")
			if err := dfi.client.Delete(ctx, &replica); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to delete pod: %w", err)
			}

			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	return ctrl.Result{}, nil
}

// updatedMaster updates the master to the latest version
func (dfi *DragonflyInstance) updatedMaster(ctx context.Context, oldMaster *corev1.Pod, replicas *corev1.PodList, updateRevision string) error {
	if len(replicas.Items) > 0 {
		newMaster, err := getUpdatedReplica(replicas, updateRevision)
		if err != nil {
			return fmt.Errorf("failed to get updated replica: %w", err)
		}

		if err = dfi.replTakeover(ctx, newMaster, oldMaster); err != nil {
			return fmt.Errorf("failed to update master: %w", err)
		}

		for _, replica := range replicas.Items {
			if replica.Name == newMaster.Name {
				continue
			}

			ready, readyErr := dfi.isPodReady(ctx, &replica)
			if readyErr != nil {
				return fmt.Errorf("failed to verify replica readiness: %w", readyErr)
			}

			if ready {
				dfi.log.Info("configuring pod as replica to the right master", "pod", replica.Name)
				if err = dfi.configureReplica(ctx, &replica, newMaster.Status.PodIP); err != nil {
					return fmt.Errorf("failed to configure pod as replica: %w", err)
				}
			}
		}
	} else {
		// delete the old master, so that it gets recreated with the new version
		dfi.log.Info("no replicas found to run REPLTAKEOVER on. deleting master", "pod", oldMaster.Name)
		if err := dfi.client.Delete(ctx, oldMaster); err != nil {
			return fmt.Errorf("failed to delete pod: %w", err)
		}
	}

	return nil
}

// replTakeover runs the replTakeOver on the given replica pod
func (dfi *DragonflyInstance) replTakeover(ctx context.Context, newMaster *corev1.Pod, oldMaster *corev1.Pod) error {
	dfi.log.Info("running REPLTAKEOVER on replica", "pod", newMaster.Name)

	redisClient := redis.NewClient(&redis.Options{
		Addr: net.JoinHostPort(newMaster.Status.PodIP, strconv.Itoa(resources.DragonflyAdminPort)),
	})
	defer redisClient.Close()

	resp, err := redisClient.Do(ctx, "repltakeover", "10000").Result()
	if err != nil {
		return fmt.Errorf("error running REPLTAKEOVER command: %w", err)
	}

	if resp != "OK" {
		return fmt.Errorf("response of `REPLTAKEOVER` on replica is not OK: %s", resp)
	}

	patchFrom := client.MergeFrom(newMaster.DeepCopy())
	newMaster.Labels[resources.RoleLabelKey] = resources.Master
	delete(newMaster.Labels, resources.MasterIpLabelKey)

	// update the label on the pod
	if err := dfi.client.Patch(ctx, newMaster, patchFrom); err != nil {
		return fmt.Errorf("failed to update the role label on the pod: %w", err)
	}

	// delete the old master, so that it gets recreated with the new version
	dfi.log.Info("deleting master", "pod", oldMaster.Name)
	if err := dfi.client.Delete(ctx, oldMaster); err != nil {
		return fmt.Errorf("failed to delete pod: %w", err)
	}

	return nil
}
