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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Dragonfly Replication Readiness Gate", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "df-readiness-gate"
	namespace := "default"

	Context("Readiness gate with 1 master + 1 replica (no snapshots)", func() {
		BeforeAll(func() {
			var existing resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &existing)
			if err == nil {
				Expect(k8sClient.Delete(ctx, &existing)).To(Succeed())
				Eventually(func() bool {
					return apierrors.IsNotFound(
						k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &existing))
				}, 1*time.Minute, 2*time.Second).Should(BeTrue())
			}

			Expect(k8sClient.Create(ctx, &resourcesv1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: resourcesv1.DragonflySpec{
					Replicas:                       2,
					EnableReplicationReadinessGate: true,
				},
			})).To(Succeed())

			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseResourcesCreated, 3*time.Minute)).To(Succeed())
			Expect(waitForStatefulSetReady(ctx, k8sClient, name, namespace, 5*time.Minute)).To(Succeed())
			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)).To(Succeed())
		})

		It("Should inject readiness gate into StatefulSet and satisfy conditions on all pods", func() {
			By("Verifying StatefulSet has the readiness gate in its pod template")
			var ss appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &ss)).To(Succeed())

			gates := ss.Spec.Template.Spec.ReadinessGates
			Expect(gates).To(HaveLen(1), "StatefulSet should have exactly one readiness gate")
			Expect(string(gates[0].ConditionType)).To(Equal(resources.ReplicationReadyConditionType))

			By("Verifying all pods have the replication-ready condition set to True")
			var pods corev1.PodList
			Expect(k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})).To(Succeed())

			Expect(pods.Items).To(HaveLen(2))

			for _, pod := range pods.Items {
				role := pod.Labels[resources.RoleLabelKey]
				Expect(role).NotTo(BeEmpty(), "pod %s should have a role label", pod.Name)

				replReady := corev1.ConditionUnknown
				podReady := corev1.ConditionUnknown
				for _, c := range pod.Status.Conditions {
					if c.Type == corev1.PodConditionType(resources.ReplicationReadyConditionType) {
						replReady = c.Status
					}
					if c.Type == corev1.PodReady {
						podReady = c.Status
					}
				}
				Expect(replReady).To(Equal(corev1.ConditionTrue),
					"pod %s (role=%s) should have replication-ready=True", pod.Name, role)
				Expect(podReady).To(Equal(corev1.ConditionTrue),
					"pod %s (role=%s) should be PodReady=True (gate satisfied)", pod.Name, role)
			}
		})

		It("Should block master eviction while replacement replica has role label but replication is not yet stable", func() {
			By("Step 1: Ensuring system is stable before test")
			Eventually(func() bool {
				master, replica, err := getMasterReplica(ctx, namespace, name)
				return err == nil && master != nil && replica != nil
			}, 60*time.Second, 1*time.Second).Should(BeTrue(),
				"System should be stable: master+replica both K8s-Ready")

			By("Starting background pod/PDB state logger")
			stopLogger := startPodStateLogger(ctx, name, namespace)
			defer stopLogger()

			By("Step 2: Finding and deleting a replica pod (wait for role to be assigned)")
			var podToDelete *corev1.Pod

			Eventually(func() bool {
				_, replica, err := getMasterReplica(ctx, namespace, name)
				if err != nil || replica == nil {
					return false
				}
				podToDelete = replica
				By(fmt.Sprintf("Found replica pod: %s", replica.Name))
				return true
			}, 30*time.Second, 1*time.Second).Should(BeTrue(), "Should eventually have a replica pod with role label")

			Expect(podToDelete).NotTo(BeNil(), "Should have found a replica pod to delete")

			By(fmt.Sprintf("Deleting replica pod: %s", podToDelete.Name))
			err := k8sClient.Delete(ctx, podToDelete)
			Expect(err).To(BeNil())

			By("Step 3: Finding master to evict")
			masterPod, _, err := getMasterReplica(ctx, namespace, name)
			Expect(err).To(BeNil())
			Expect(masterPod).NotTo(BeNil(), "Master pod should still be running")

			By("Step 3b: Detecting the recovery window — replica has role=replica but readiness gate is False")
			deletedName := podToDelete.Name
			deletedUID := podToDelete.UID
			raceWindowSeen := false
			var evictErr error

			for i := 0; i < 600; i++ {
				var pods corev1.PodList
				if listErr := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
				}); listErr != nil {
					time.Sleep(10 * time.Millisecond)
					continue
				}

				for i := range pods.Items {
					p := &pods.Items[i]
					if p.Name != deletedName || p.UID == deletedUID {
						continue
					}
					if p.Labels[resources.RoleLabelKey] != resources.Replica {
						continue
					}
					podReady := false
					for _, c := range p.Status.Conditions {
						if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
							podReady = true
						}
					}
					if podReady {
						continue
					}

					raceWindowSeen = true
					evictErr = tryEvictPod(ctx, masterPod)
					GinkgoWriter.Printf("[race window] replacement %s (uid=%s) role=replica ready=false, eviction blocked=%v err=%v\n",
						p.Name, p.UID, apierrors.IsTooManyRequests(evictErr), evictErr)
				}

				if raceWindowSeen {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}

			Expect(raceWindowSeen).To(BeTrue(), "Replacement replica should have become ready within 60s")
			Expect(apierrors.IsTooManyRequests(evictErr)).To(BeTrue(),
				"Master eviction must be rejected (HTTP 429) by the PDB during recovery — replication may still be in progress")

			By("Step 4: Waiting for pod to be recreated and replication to stabilize")
			Expect(waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)).To(Succeed())

			By("Step 5: Verifying both pods are K8s-Ready (readiness gate satisfied, replication stable)")
			Eventually(func() bool {
				master, replica, err := getMasterReplica(ctx, namespace, name)
				return err == nil && master != nil && replica != nil
			}, 30*time.Second, 2*time.Second).Should(BeTrue(), "Both pods should be K8s-Ready after replication stabilises")

			By("Step 6: Attempting to evict master after recovery (should now be allowed)")
			masterPod, _, err = getMasterReplica(ctx, namespace, name)
			Expect(err).To(BeNil())
			Expect(masterPod).NotTo(BeNil(), "Master pod should exist after recovery")
			Expect(tryEvictPod(ctx, masterPod)).To(Succeed(),
				"Master eviction should be allowed when replica's readiness gate is satisfied (replication stable)")

			By("Step 7: Waiting for system to recover after master eviction")
			Expect(waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)).To(Succeed())
			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 1*time.Minute)).To(BeNil())
		})

		It("Should transition replica readiness condition to True after sync completes", func() {
			By("Step 1: Ensuring system is stable")
			Eventually(func() bool {
				master, replica, err := getMasterReplica(ctx, namespace, name)
				return err == nil && master != nil && replica != nil
			}, 60*time.Second, 1*time.Second).Should(BeTrue(),
				"System should be stable before test")

			By("Step 2: Deleting a replica pod")
			_, replica, err := getMasterReplica(ctx, namespace, name)
			Expect(err).To(BeNil())
			Expect(replica).NotTo(BeNil())

			deletedName := replica.Name
			deletedUID := replica.UID
			By(fmt.Sprintf("Deleting replica pod: %s (uid=%s)", deletedName, deletedUID))
			Expect(k8sClient.Delete(ctx, replica)).To(Succeed())

			By("Step 3: Observing replacement pod condition lifecycle")
			conditionObservedFalse := false
			Eventually(func() bool {
				var pods corev1.PodList
				if listErr := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
				}); listErr != nil {
					return false
				}

				for i := range pods.Items {
					p := &pods.Items[i]
					if p.Name != deletedName || p.UID == deletedUID {
						continue
					}
					if p.Labels[resources.RoleLabelKey] != resources.Replica {
						continue
					}

					hasCondition := false
					for _, c := range p.Status.Conditions {
						if c.Type == corev1.PodConditionType(resources.ReplicationReadyConditionType) {
							hasCondition = true
							if c.Status == corev1.ConditionFalse {
								conditionObservedFalse = true
								GinkgoWriter.Printf("[transition] replacement %s replication-ready=False (sync in progress)\n", p.Name)
							} else if c.Status == corev1.ConditionTrue {
								GinkgoWriter.Printf("[transition] replacement %s replication-ready=True\n", p.Name)
								return true
							}
						}
					}
					if !hasCondition {
						conditionObservedFalse = true
						GinkgoWriter.Printf("[transition] replacement %s replication-ready condition absent\n", p.Name)
					}
				}
				return false
			}, 60*time.Second, 100*time.Millisecond).Should(BeTrue(),
				"Replacement replica should eventually have replication-ready=True")

			if conditionObservedFalse {
				GinkgoWriter.Printf("[transition] observed False/absent state before True (sync was slow enough to catch)\n")
			} else {
				GinkgoWriter.Printf("[transition] condition went directly to True (sync completed before observation)\n")
			}

			By("Step 4: Verifying PodReady is also True (gate no longer blocking)")
			Eventually(func() bool {
				var pod corev1.Pod
				if getErr := k8sClient.Get(ctx, types.NamespacedName{Name: deletedName, Namespace: namespace}, &pod); getErr != nil {
					return false
				}
				for _, c := range pod.Status.Conditions {
					if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
						return true
					}
				}
				return false
			}, 30*time.Second, 1*time.Second).Should(BeTrue(),
				"PodReady should be True once the readiness gate is satisfied")

			By("Step 5: Verifying Dragonfly CR returns to PhaseReady")
			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)).To(Succeed())
		})

		AfterAll(func() {
			var df resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &df)
			if apierrors.IsNotFound(err) {
				return
			}
			Expect(err).To(BeNil())
			Expect(k8sClient.Delete(ctx, &df)).To(Succeed())
			Eventually(func() bool {
				return apierrors.IsNotFound(
					k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &df))
			}, 1*time.Minute, 2*time.Second).Should(BeTrue(), "resource should be deleted")
		})
	})
})
