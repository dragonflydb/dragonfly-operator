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
	"time"

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/controller"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("DF Pod Lifecycle Reconciler", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	var podRoles map[string][]string
	name := "health-test"
	namespace := "default"
	replicas := 4

	df := dfv1alpha1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: dfv1alpha1.DragonflySpec{
			Replicas: int32(replicas),
		},
	}

	Context("Fail Over is working", func() {
		It("Initial Master is elected", func() {
			err := k8sClient.Create(ctx, &df)
			Expect(err).To(BeNil())
		})

		It("Check for resources, functional pods and status", func() {

			// Wait until Dragonfly object is marked initialized
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseResourcesCreated, 2*time.Minute)
			waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)

			// Check for service and statefulset
			var ss appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &ss)
			Expect(err).To(BeNil())

			var svc corev1.Service
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &svc)
			Expect(err).To(BeNil())

			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 1*time.Minute)
			Expect(err).To(BeNil())

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 4 pod replicas = 1 master + 3 replicas
			Expect(pods.Items).To(HaveLen(replicas))

			// Reset podRoles map for each test attempt (needed for FlakeAttempts retries)
			podRoles = map[string][]string{
				resources.Master:  make([]string, 0),
				resources.Replica: make([]string, 0),
			}

			// Get the pods along with their roles
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				// error if there is no label
				Expect(ok).To(BeTrue())

				podRoles[role] = append(podRoles[role], pod.Name)
			}

			// One Master & Three Replicas
			Expect(podRoles[resources.Master]).To(HaveLen(1))
			Expect(podRoles[resources.Replica]).To(HaveLen(replicas - 1))
		})

		It("Delete old master", func() {

			// Get & Delete the old master
			var pod corev1.Pod
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      podRoles[resources.Master][0],
			}, &pod)
			Expect(err).To(BeNil())

			err = k8sClient.Delete(ctx, &pod)
			Expect(err).To(BeNil())
		})

		It("New master is elected", func() {

			// Wait until the loop is reconciled. This is needed as status is ready previously
			// and the test might move forward even before the reconcile loop is triggered
			time.Sleep(1 * time.Minute)

			err := waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)
			Expect(err).To(BeNil())

			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)
			Expect(err).To(BeNil())

			err = waitForAllPodsToHaveRoleLabels(ctx, k8sClient, name, namespace, replicas, 2*time.Minute)
			Expect(err).To(BeNil())

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 4 pod replicas = 1 master + 3 replicas
			Expect(pods.Items).To(HaveLen(replicas))

			// Get the pods along with their roles
			podRoles := make(map[string][]string)
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				// error if there is no label
				Expect(ok).To(BeTrue())
				// verify the role to match the label
				podRoles[role] = append(podRoles[role], pod.Name)
			}

			// One Master & Three Replicas
			Expect(podRoles[resources.Master]).To(HaveLen(1))
			Expect(podRoles[resources.Replica]).To(HaveLen(replicas - 1))
		})

		It("New pods are added as replica", func() {
			var pod corev1.Pod
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      podRoles[resources.Replica][0],
			}, &pod)
			Expect(err).To(BeNil())

			err = k8sClient.Delete(ctx, &pod)
			Expect(err).To(BeNil())

			// Wait until the loop is reconciled. This is needed as status is ready previously
			// and the test might move forward even before the reconcile loop is triggered
			time.Sleep(10 * time.Second)

			// Expect a new replica
			// Wait for Status to be ready
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)
			Expect(err).To(BeNil())

			err = waitForAllPodsToHaveRoleLabels(ctx, k8sClient, name, namespace, replicas, 2*time.Minute)
			Expect(err).To(BeNil())

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 4 pod replicas = 1 master + 3 replicas
			Expect(pods.Items).To(HaveLen(replicas))

			// Get the pods along with their roles
			podRoles := make(map[string][]string)
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				// error if there is no label
				Expect(ok).To(BeTrue())
				// verify the role to match the label
				podRoles[role] = append(podRoles[role], pod.Name)
			}

			// One Master & Three Replicas
			Expect(podRoles[resources.Master]).To(HaveLen(1))
			Expect(podRoles[resources.Replica]).To(HaveLen(replicas - 1))
		})

		It("Cleanup", func() {
			var df dfv1alpha1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			err = k8sClient.Delete(ctx, &df)
			Expect(err).To(BeNil())
		})
	})

})

var _ = Describe("DF Failover Under Load", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "failover-load-test"
	namespace := "default"
	replicas := int32(3)

	Context("Write continuity during master failover", func() {
		It("Should create Dragonfly instance", func() {
			// Clean up any leftover from previous test runs
			var existingDf dfv1alpha1.Dragonfly
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &existingDf); err == nil {
				_ = k8sClient.Delete(ctx, &existingDf)
				Eventually(func() bool {
					var pods corev1.PodList
					err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
						resources.DragonflyNameLabelKey: name,
					})
					return err == nil && len(pods.Items) == 0
				}, 2*time.Minute, 5*time.Second).Should(BeTrue())
			}

			df := dfv1alpha1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: dfv1alpha1.DragonflySpec{
					Replicas: replicas,
				},
			}
			err := k8sClient.Create(ctx, &df)
			Expect(err).To(BeNil())

			// Wait for ready state
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())
		})

		It("Should handle writes during master failover with minimal errors", func() {
			// Connect to the cluster
			stopChan := make(chan struct{}, 1)
			rc, err := checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6397)
			Expect(err).To(BeNil())
			defer close(stopChan)
			defer rc.Close()

			// Start continuous writes in background (write every 10ms for 2 minutes)
			writeCtx, writeCancel := context.WithCancel(ctx)
			resultChan, _ := runContinuousWritesAsync(writeCtx, rc, 2*time.Minute, 10*time.Millisecond)

			// Wait a bit for writes to start
			time.Sleep(5 * time.Second)

			// Find and delete the master pod
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey: name,
				resources.RoleLabelKey:          resources.Master,
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(1))

			masterPod := pods.Items[0]
			oldMasterIP := masterPod.Status.PodIP
			GinkgoLogr.Info("Deleting master pod to trigger failover", "pod", masterPod.Name)
			err = k8sClient.Delete(ctx, &masterPod)
			Expect(err).To(BeNil())

			transition, err := waitForEndpointTransition(ctx, k8sClient, name, namespace, oldMasterIP, 2*time.Minute)
			Expect(err).To(BeNil())
			Expect(transition.OldRemovedAt).NotTo(BeZero())
			Expect(transition.NewAddedAt).NotTo(BeZero())
			Expect(transition.OldRemovedAt.After(transition.NewAddedAt)).To(BeFalse(), "old master should be removed before or at the same time as new master is added")

			// Wait for failover to complete
			time.Sleep(1 * time.Minute)

			// Verify cluster is ready again
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)
			Expect(err).To(BeNil())

			// Verify that the new master matches the endpoint transition
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey: name,
				resources.RoleLabelKey:          resources.Master,
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(1))
			Expect(pods.Items[0].Status.PodIP).To(Equal(transition.NewIP))

			// Stop writes and get results
			writeCancel()
			result := <-resultChan

			GinkgoLogr.Info("Write test results",
				"total", result.TotalWrites,
				"success", result.SuccessWrites,
				"failed", result.FailedWrites,
				"readOnlyErrors", result.ReadOnlyErrors,
			)

			// Verify results - allow some errors during failover window but READONLY should be minimal
			Expect(result.TotalWrites).To(BeNumerically(">", 20), "should have performed writes during failover")
			Expect(result.ReadOnlyErrors).To(BeNumerically("<", 10), "READONLY errors should be minimal with traffic gating")

			// Log other errors for debugging
			if len(result.OtherErrors) > 0 {
				GinkgoLogr.Info("Other errors encountered", "count", len(result.OtherErrors), "sample", result.OtherErrors[0])
			}
		})

		It("Cleanup", func() {
			var df dfv1alpha1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			err = k8sClient.Delete(ctx, &df)
			Expect(err).To(BeNil())

			// Wait for pods to terminate
			Eventually(func() bool {
				var pods corev1.PodList
				err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey: name,
				})
				return err == nil && len(pods.Items) == 0
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())
		})
	})
})

var _ = Describe("DF Rolling Update Under Load", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "rollout-load-test"
	namespace := "default"
	replicas := int32(3)

	Context("Write continuity during rolling update", func() {
		It("Should create Dragonfly instance", func() {
			// Clean up any leftover from previous test runs
			var existingDf dfv1alpha1.Dragonfly
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &existingDf); err == nil {
				_ = k8sClient.Delete(ctx, &existingDf)
				Eventually(func() bool {
					var pods corev1.PodList
					err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
						resources.DragonflyNameLabelKey: name,
					})
					return err == nil && len(pods.Items) == 0
				}, 2*time.Minute, 5*time.Second).Should(BeTrue())
			}

			df := dfv1alpha1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: dfv1alpha1.DragonflySpec{
					Replicas: replicas,
				},
			}
			err := k8sClient.Create(ctx, &df)
			Expect(err).To(BeNil())

			// Wait for ready state
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())
		})

		It("Should handle writes during rolling update with minimal errors", func() {
			// Connect to the cluster
			stopChan := make(chan struct{}, 1)
			rc, err := checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6398)
			Expect(err).To(BeNil())
			cleanupPortForward := func() {
				if stopChan != nil {
					close(stopChan)
					stopChan = nil
				}
				if rc != nil {
					rc.Close()
					rc = nil
				}
			}
			defer cleanupPortForward()

			// Insert some test data first
			err = rc.Set(ctx, "pre-rollout-key", "pre-rollout-value", 0).Err()
			Expect(err).To(BeNil())

			// Capture current master IP before rollout
			var masterPods corev1.PodList
			err = k8sClient.List(ctx, &masterPods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey: name,
				resources.RoleLabelKey:          resources.Master,
			})
			Expect(err).To(BeNil())
			Expect(masterPods.Items).To(HaveLen(1))
			oldMasterIP := masterPods.Items[0].Status.PodIP

			// Start continuous writes in background (write every 10ms for 4 minutes to cover rollout)
			writeCtx, writeCancel := context.WithCancel(ctx)
			resultChan, _ := runContinuousWritesAsync(writeCtx, rc, 4*time.Minute, 10*time.Millisecond)

			// Wait a bit for writes to start
			time.Sleep(5 * time.Second)

			// Trigger rolling update by changing the image
			var currentDf dfv1alpha1.Dragonfly
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &currentDf)
			Expect(err).To(BeNil())

			GinkgoLogr.Info("Triggering rolling update by changing image")
			currentDf.Spec.Image = resources.DragonflyImage + ":v1.30.3"
			err = k8sClient.Update(ctx, &currentDf)
			Expect(err).To(BeNil())

			transitionCtx, transitionCancel := context.WithCancel(ctx)
			defer transitionCancel()
			transitionCh := make(chan *EndpointTransition, 1)
			transitionErrCh := make(chan error, 1)
			go func() {
				transition, transitionErr := waitForEndpointTransition(transitionCtx, k8sClient, name, namespace, oldMasterIP, 6*time.Minute)
				if transitionErr != nil {
					transitionErrCh <- transitionErr
					return
				}
				transitionCh <- transition
			}()

			// Wait for rolling update to complete
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 5*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 5*time.Minute)
			Expect(err).To(BeNil())

			// Verify all pods have the new image
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey: name,
			})
			Expect(err).To(BeNil())
			for _, pod := range pods.Items {
				Expect(pod.Spec.Containers[0].Image).To(Equal(currentDf.Spec.Image))
			}

			// Verify that the master matches the endpoint transition
			err = k8sClient.List(ctx, &masterPods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey: name,
				resources.RoleLabelKey:          resources.Master,
			})
			Expect(err).To(BeNil())
			Expect(masterPods.Items).To(HaveLen(1))
			newMasterIP := masterPods.Items[0].Status.PodIP

			if newMasterIP == oldMasterIP {
				transitionCancel()
			} else {
				select {
				case transition := <-transitionCh:
					Expect(transition.OldRemovedAt).NotTo(BeZero())
					Expect(transition.NewAddedAt).NotTo(BeZero())
					Expect(transition.OldRemovedAt.After(transition.NewAddedAt)).To(BeFalse(), "old master should be removed before or at the same time as new master is added")
					Expect(transition.NewIP).To(Equal(newMasterIP))
				case err := <-transitionErrCh:
					if err != context.Canceled {
						Expect(err).To(BeNil())
					}
				case <-time.After(30 * time.Second):
					Expect(false).To(BeTrue(), "timed out waiting for endpoint transition")
				}
			}

			// If the master IP didn't change, a transition may not have occurred.
			if newMasterIP == oldMasterIP {
				select {
				case err := <-transitionErrCh:
					if err != context.Canceled && err != context.DeadlineExceeded {
						Expect(err).To(BeNil())
					}
				default:
				}
			}

			// Stop writes and get results
			writeCancel()
			result := <-resultChan

			GinkgoLogr.Info("Rolling update write test results",
				"total", result.TotalWrites,
				"success", result.SuccessWrites,
				"failed", result.FailedWrites,
				"readOnlyErrors", result.ReadOnlyErrors,
			)

			// Verify results
			Expect(result.ReadOnlyErrors).To(BeNumerically("<", 10), "READONLY errors should be minimal with traffic gating")

			// Re-establish port-forward after rollout to avoid stale connection
			cleanupPortForward()
			stopChan = make(chan struct{}, 1)
			rc, err = checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6398)
			Expect(err).To(BeNil())

			// Verify data integrity - pre-rollout data should still exist
			val, err := rc.Get(ctx, "pre-rollout-key").Result()
			Expect(err).To(BeNil())
			Expect(val).To(Equal("pre-rollout-value"))
		})

		It("Cleanup", func() {
			var df dfv1alpha1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			err = k8sClient.Delete(ctx, &df)
			Expect(err).To(BeNil())

			// Wait for pods to terminate
			Eventually(func() bool {
				var pods corev1.PodList
				err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey: name,
				})
				return err == nil && len(pods.Items) == 0
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())
		})
	})
})

var _ = Describe("DF Traffic Label Edge Cases", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "traffic-edge-test"
	namespace := "default"
	replicas := int32(3)

	Context("Traffic label consistency", func() {
		It("Should create Dragonfly instance", func() {
			// Clean up any leftover from previous test runs
			var existingDf dfv1alpha1.Dragonfly
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &existingDf); err == nil {
				_ = k8sClient.Delete(ctx, &existingDf)
				Eventually(func() bool {
					var pods corev1.PodList
					err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
						resources.DragonflyNameLabelKey: name,
					})
					return err == nil && len(pods.Items) == 0
				}, 2*time.Minute, 5*time.Second).Should(BeTrue())
			}

			df := dfv1alpha1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: dfv1alpha1.DragonflySpec{
					Replicas: replicas,
				},
			}
			err := k8sClient.Create(ctx, &df)
			Expect(err).To(BeNil())

			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())
		})

		It("Should have correct traffic labels on master and replicas", func() {
			var pods corev1.PodList
			err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey: name,
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(int(replicas)))

			masterCount := 0
			for _, pod := range pods.Items {
				role := pod.Labels[resources.RoleLabelKey]
				traffic := pod.Labels[resources.TrafficLabelKey]

				if role == resources.Master {
					masterCount++
					Expect(traffic).To(Equal(resources.TrafficEnabled), "master should have traffic enabled")
				} else if role == resources.Replica {
					Expect(traffic).To(Equal(resources.TrafficDisabled), "replica should have traffic disabled")
				}
			}
			Expect(masterCount).To(Equal(1), "should have exactly one master")
		})

		It("Service selector should include traffic label", func() {
			var svc corev1.Service
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &svc)
			Expect(err).To(BeNil())

			Expect(svc.Spec.Selector).To(HaveKeyWithValue(resources.TrafficLabelKey, resources.TrafficEnabled))
			Expect(svc.Spec.Selector).To(HaveKeyWithValue(resources.RoleLabelKey, resources.Master))
		})

		It("Endpoints should only contain master IP", func() {
			// Get the master pod IP
			var masterPods corev1.PodList
			err := k8sClient.List(ctx, &masterPods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey: name,
				resources.RoleLabelKey:          resources.Master,
			})
			Expect(err).To(BeNil())
			Expect(masterPods.Items).To(HaveLen(1))
			masterIP := masterPods.Items[0].Status.PodIP

			// Check endpoints
			var endpoints corev1.Endpoints
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &endpoints)
			Expect(err).To(BeNil())

			// Should have exactly one address (the master)
			totalAddresses := 0
			for _, subset := range endpoints.Subsets {
				for _, addr := range subset.Addresses {
					totalAddresses++
					Expect(addr.IP).To(Equal(masterIP), "endpoint should only contain master IP")
				}
			}
			Expect(totalAddresses).To(Equal(1), "should have exactly one endpoint address")
		})

		It("Cleanup", func() {
			var df dfv1alpha1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			err = k8sClient.Delete(ctx, &df)
			Expect(err).To(BeNil())

			Eventually(func() bool {
				var pods corev1.PodList
				err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey: name,
				})
				return err == nil && len(pods.Items) == 0
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())
		})
	})
})

var _ = Describe("DF Service Name Override", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "svc-override-test"
	customServiceName := "custom-dragonfly-svc"
	namespace := "default"
	replicas := int32(2)

	Context("Custom service name with traffic gating", func() {
		It("Should create Dragonfly with custom service name", func() {
			// Clean up any leftover from previous test runs
			var existingDf dfv1alpha1.Dragonfly
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &existingDf); err == nil {
				_ = k8sClient.Delete(ctx, &existingDf)
				Eventually(func() bool {
					var pods corev1.PodList
					err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
						resources.DragonflyNameLabelKey: name,
					})
					return err == nil && len(pods.Items) == 0
				}, 2*time.Minute, 5*time.Second).Should(BeTrue())
			}

			df := dfv1alpha1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: dfv1alpha1.DragonflySpec{
					Replicas: replicas,
					ServiceSpec: &dfv1alpha1.ServiceSpec{
						Name: customServiceName,
					},
				},
			}
			err := k8sClient.Create(ctx, &df)
			Expect(err).To(BeNil())

			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())
		})

		It("Should create service with custom name and traffic selector", func() {
			var svc corev1.Service
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      customServiceName,
				Namespace: namespace,
			}, &svc)
			Expect(err).To(BeNil())

			Expect(svc.Spec.Selector).To(HaveKeyWithValue(resources.TrafficLabelKey, resources.TrafficEnabled))
		})

		It("Should have endpoints on custom service name", func() {
			var endpoints corev1.Endpoints
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      customServiceName,
				Namespace: namespace,
			}, &endpoints)
			Expect(err).To(BeNil())

			// Should have exactly one address (the master)
			totalAddresses := 0
			for _, subset := range endpoints.Subsets {
				totalAddresses += len(subset.Addresses)
			}
			Expect(totalAddresses).To(Equal(1))
		})

		It("Cleanup", func() {
			var df dfv1alpha1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			err = k8sClient.Delete(ctx, &df)
			Expect(err).To(BeNil())

			Eventually(func() bool {
				var pods corev1.PodList
				err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey: name,
				})
				return err == nil && len(pods.Items) == 0
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())
		})
	})
})
