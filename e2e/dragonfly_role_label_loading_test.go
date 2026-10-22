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

package e2e

import (
	"context"
	"fmt"
	"time"

	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/controller"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/redis/go-redis/v9"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// This suite guards the fix for role-label reconciliation during dataset
// loads: a pod whose Dragonfly admin socket is reachable must (re)gain its
// role=replica label immediately, even while the dataset is still loading and
// the pod is therefore not "ready" by the operator's stricter dataset-loaded
// check. Before the fix, the pod lifecycle reconciler returned early on
// !podReady, so loading pods stayed unlabeled until loading completed.
//
// The Dragonfly instance is intentionally CPU-constrained and seeded with a
// large keyspace so that a restarted pod spends an observable amount of time
// in the loading state (snapshot load followed by a full sync).
var _ = Describe("Dragonfly Role Label Recovery While Loading", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "df-loading-label-test"
	namespace := "default"

	const (
		seedChunks        = 5
		keysPerChunk      = 200000
		seedValueSizeArg  = "128"
		samplingInterval  = 300 * time.Millisecond
		missedWindowLimit = 5
	)

	Context("Role label is reconciled while the dataset is loading", func() {
		BeforeAll(func() {
			// Clean up any existing resource from previous test runs
			var df resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			if apierrors.IsNotFound(err) {
				return
			}
			Expect(err).To(BeNil(), "unexpected error getting Dragonfly resource")
			Expect(k8sClient.Delete(ctx, &df)).To(Succeed(), "failed to delete existing Dragonfly resource")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: namespace,
				}, &df)
				return apierrors.IsNotFound(err)
			}, 1*time.Minute, 2*time.Second).Should(BeTrue(), "existing resource should be deleted")
		})

		It("Should create a CPU-constrained Dragonfly with snapshot persistence", func() {
			err := k8sClient.Create(ctx, &resourcesv1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: resourcesv1.DragonflySpec{
					Replicas: 2,
					// The readiness gate keeps PodReady=False until the
					// replica reaches a stable sync, which lets this test
					// assert that PodReady is still blocked while the role
					// label is already reconciled.
					EnableReplicationReadinessGate: true,
					// Throttle the CPU so snapshot loading and full syncs
					// take long enough to be observable from the test.
					Resources: &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("150m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("150m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
					Snapshot: &resourcesv1.Snapshot{
						Cron: "*/1 * * * *",
						PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{
								corev1.ReadWriteOnce,
							},
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			})
			Expect(err).To(BeNil())

			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseResourcesCreated, 3*time.Minute)).
				To(Succeed(), "Dragonfly should reach PhaseResourcesCreated")
			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 5*time.Minute)).
				To(Succeed(), "Dragonfly should reach PhaseReady")
			Expect(waitForStatefulSetReady(ctx, k8sClient, name, namespace, 5*time.Minute)).
				To(Succeed(), "StatefulSet should become ready")
		})

		It("Should seed a dataset large enough to produce a visible load window", func() {
			Expect(waitForMasterPod(ctx, k8sClient, name, namespace, 2*time.Minute)).To(Succeed())

			var masterPods corev1.PodList
			Expect(k8sClient.List(ctx, &masterPods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey: name,
				resources.RoleLabelKey:          resources.Master,
			})).To(Succeed())
			Expect(masterPods.Items).To(HaveLen(1), "exactly one master pod should exist")

			pf, err := setupPortForwardWithCleanup(ctx, clientset, cfg, &masterPods.Items[0], resources.DragonflyPort, 30*time.Second)
			Expect(err).To(BeNil())
			defer pf.Cleanup()

			// DEBUG POPULATE generates the keys server side, but the
			// CPU-throttled instance needs a while per chunk, hence the
			// generous timeouts.
			rc := redis.NewClient(&redis.Options{
				Addr:                  fmt.Sprintf("localhost:%d", pf.LocalPort),
				DialTimeout:           15 * time.Second,
				ReadTimeout:           2 * time.Minute,
				WriteTimeout:          30 * time.Second,
				ContextTimeoutEnabled: true,
			})
			defer rc.Close()

			for i := 0; i < seedChunks; i++ {
				err := rc.Do(ctx, "DEBUG", "POPULATE", keysPerChunk, fmt.Sprintf("chunk%d-", i), seedValueSizeArg).Err()
				Expect(err).To(BeNil(), "DEBUG POPULATE chunk %d should succeed", i)
			}

			dbSize, err := rc.DBSize(ctx).Result()
			Expect(err).To(BeNil())
			Expect(dbSize).To(BeNumerically(">=", int64(seedChunks*keysPerChunk)), "master should hold the seeded keys")
			GinkgoLogr.Info("Dataset seeded", "keys", dbSize)

			// The snapshot cron fires every minute (on both master and
			// replica); waiting a bit longer than one full cycle guarantees
			// every pod has persisted the seeded dataset to its PVC.
			time.Sleep(75 * time.Second)
		})

		It("Should reapply role=replica while the dataset is still loading", func() {
			// Make sure the cluster is settled before disturbing it. This also
			// lets flake retries of this spec start from a clean state.
			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 5*time.Minute)).To(Succeed())
			Expect(waitForStatefulSetReady(ctx, k8sClient, name, namespace, 5*time.Minute)).To(Succeed())

			var replicaPods corev1.PodList
			Expect(k8sClient.List(ctx, &replicaPods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
				resources.RoleLabelKey:             resources.Replica,
			})).To(Succeed())
			Expect(replicaPods.Items).NotTo(BeEmpty(), "a replica pod should exist")

			target := replicaPods.Items[0]
			podName := target.Name
			podKey := types.NamespacedName{Name: podName, Namespace: namespace}
			oldUID := target.UID

			// Restart the replica. The recreated pod finds the persisted
			// snapshot on its PVC and goes through a real dataset-load window
			// (snapshot load, then a full sync once SLAVE OF is accepted).
			Expect(k8sClient.Delete(ctx, &target)).To(Succeed())

			// Wait until the new pod is Running and its dragonfly container is
			// ready, i.e. the admin socket is reachable. This mirrors the
			// operator's isReachable() gate and is intentionally weaker than
			// its dataset-loaded readiness check.
			var restarted corev1.Pod
			Eventually(func() error {
				if err := k8sClient.Get(ctx, podKey, &restarted); err != nil {
					return err
				}
				if restarted.UID == oldUID {
					return fmt.Errorf("pod %s not recreated yet", podName)
				}
				if restarted.Status.Phase != corev1.PodRunning {
					return fmt.Errorf("pod %s not running yet (phase: %s)", podName, restarted.Status.Phase)
				}
				if !dragonflyContainerIsReady(&restarted) {
					return fmt.Errorf("dragonfly container in pod %s not ready yet", podName)
				}
				return nil
			}, 3*time.Minute, 1*time.Second).Should(Succeed())

			// Keep a single port-forward to the admin port so the loading
			// state can be sampled at high frequency.
			pf, err := setupPortForwardWithCleanup(ctx, clientset, cfg, &restarted, resources.DragonflyAdminPort, 30*time.Second)
			Expect(err).To(BeNil())
			defer pf.Cleanup()

			adminClient := redis.NewClient(&redis.Options{
				Addr:                  fmt.Sprintf("localhost:%d", pf.LocalPort),
				DialTimeout:           5 * time.Second,
				ReadTimeout:           5 * time.Second,
				WriteTimeout:          5 * time.Second,
				ContextTimeoutEnabled: true,
			})
			defer adminClient.Close()

			sampleLoading := func() (bool, error) {
				infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				info, err := adminClient.Info(infoCtx, "persistence").Result()
				if err != nil {
					return false, err
				}
				loading, loadState := parseLoadingInfo(info)
				return isDatasetLoading(loading, loadState), nil
			}

			// Step 1 — assert the intermediate state the fix creates: the pod
			// gets role=replica while INFO persistence still reports the
			// dataset as loading. The pre-fix reconciler returned early on
			// !podReady, so a loading pod stayed unlabeled until loading
			// completed — which the final-state checks of the other dataset
			// loading test cannot detect.
			observedRoleWhileLoading := false
			podReadyDuringWindow := corev1.ConditionUnknown
			missedWindowStreak := 0

			deadline := time.Now().Add(4 * time.Minute)
			for time.Now().Before(deadline) {
				stillLoading, err := sampleLoading()
				if err != nil {
					// Transient connection hiccup; retry.
					time.Sleep(samplingInterval)
					continue
				}

				var pod corev1.Pod
				if err := k8sClient.Get(ctx, podKey, &pod); err != nil {
					time.Sleep(samplingInterval)
					continue
				}

				if role, hasRole := pod.Labels[resources.RoleLabelKey]; hasRole {
					if stillLoading {
						Expect(role).To(Equal(resources.Replica), "restarted pod should be labeled as replica")
						podReadyDuringWindow = podCondition(&pod, corev1.PodReady)
						observedRoleWhileLoading = true
						break
					}
					// The label is present but no load is in progress. There
					// can be a short gap between the initial snapshot load and
					// the full sync that follows SLAVE OF, so tolerate a few
					// samples before concluding the window was missed.
					missedWindowStreak++
					if missedWindowStreak >= missedWindowLimit {
						break
					}
				} else {
					missedWindowStreak = 0
				}

				time.Sleep(samplingInterval)
			}

			Expect(observedRoleWhileLoading).To(BeTrue(),
				"pod should be labeled role=replica while the dataset is still loading; with the old behavior loading pods stayed unlabeled until loading completed")

			// While the dataset is loading the pod must not be PodReady: the
			// replication-ready readiness gate only opens once the replica
			// reaches a stable sync, which requires loading to have finished.
			Expect(podReadyDuringWindow).NotTo(Equal(corev1.ConditionTrue),
				"PodReady must still be blocked by the readiness gate while the dataset is loading")

			// Step 2 — simulate a lost label patch (e.g. a transient API
			// outage) inside the load window and verify the operator restores
			// the label without waiting for loading to complete.
			var podToStrip corev1.Pod
			Expect(k8sClient.Get(ctx, podKey, &podToStrip)).To(Succeed())
			stripPatch := client.MergeFrom(podToStrip.DeepCopy())
			delete(podToStrip.Labels, resources.RoleLabelKey)
			Expect(k8sClient.Patch(ctx, &podToStrip, stripPatch)).To(Succeed())

			restored := false
			restoredWhileLoading := false
			deadline = time.Now().Add(2 * time.Minute)
			for time.Now().Before(deadline) {
				stillLoading, err := sampleLoading()
				if err != nil {
					time.Sleep(samplingInterval)
					continue
				}

				var pod corev1.Pod
				if err := k8sClient.Get(ctx, podKey, &pod); err != nil {
					time.Sleep(samplingInterval)
					continue
				}

				if role, hasRole := pod.Labels[resources.RoleLabelKey]; hasRole {
					Expect(role).To(Equal(resources.Replica), "restored label should be role=replica")
					restored = true
					restoredWhileLoading = stillLoading
					break
				}

				if !stillLoading {
					// The load window closed before the operator restored the
					// label; the assertions below will fail and report it.
					break
				}

				time.Sleep(samplingInterval)
			}

			Expect(restored).To(BeTrue(), "operator should restore a stripped role label")
			Expect(restoredWhileLoading).To(BeTrue(),
				"the role label must be restored while the dataset is still loading, not only after loading completes")

			// Step 3 — everything converges once loading completes: the
			// replica keeps its label, reaches a stable sync so the readiness
			// gate opens, and the cluster reports Ready again.
			Eventually(func() error {
				var pod corev1.Pod
				if err := k8sClient.Get(ctx, podKey, &pod); err != nil {
					return err
				}
				if pod.Labels[resources.RoleLabelKey] != resources.Replica {
					return fmt.Errorf("pod %s lost its replica role label", podName)
				}
				if podCondition(&pod, corev1.PodReady) != corev1.ConditionTrue {
					return fmt.Errorf("pod %s is not PodReady yet", podName)
				}
				return nil
			}, 5*time.Minute, 2*time.Second).Should(Succeed(), "replica should become PodReady with its role label after loading completes")

			Expect(waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)).To(Succeed())
			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)).To(Succeed())
		})

		AfterAll(func() {
			var df resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			if apierrors.IsNotFound(err) {
				return
			}
			Expect(err).To(BeNil(), "unexpected error getting Dragonfly resource during cleanup")
			Expect(k8sClient.Delete(ctx, &df)).To(Succeed(), "failed to delete Dragonfly resource during cleanup")
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: namespace,
				}, &df)
				return apierrors.IsNotFound(err)
			}, 1*time.Minute, 2*time.Second).Should(BeTrue(), "resource should be deleted")
		})
	})
})

// dragonflyContainerIsReady returns true if the dragonfly container of the pod
// passes its readiness probe.
func dragonflyContainerIsReady(pod *corev1.Pod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == resources.DragonflyContainerName {
			return cs.Ready
		}
	}
	return false
}

// podCondition returns the status of the given pod condition, or
// ConditionUnknown when the condition is absent.
func podCondition(pod *corev1.Pod, condType corev1.PodConditionType) corev1.ConditionStatus {
	for _, c := range pod.Status.Conditions {
		if c.Type == condType {
			return c.Status
		}
	}
	return corev1.ConditionUnknown
}
