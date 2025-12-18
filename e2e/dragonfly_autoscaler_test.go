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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Dragonfly Autoscaler Tests", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "df-autoscaler-test"
	namespace := "default"

	var df *resourcesv1.Dragonfly

	// Resource requirements for autoscaler tests
	resourcesReq := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("300Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("400Mi"),
		},
	}

	// Cleanup function that will be called even if tests fail
	cleanupResources := func() {
		if df != nil {
			// Try to delete the Dragonfly resource
			err := k8sClient.Delete(ctx, df)
			if err != nil {
				fmt.Printf("Warning: Failed to delete Dragonfly resource: %v\n", err)
			} else {
				fmt.Printf("Successfully deleted Dragonfly resource: %s\n", name)
			}

			// Wait for cleanup to complete
			Eventually(func() bool {
				var existingDf resourcesv1.Dragonfly
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: namespace,
				}, &existingDf)
				return err != nil // Return true when resource is gone
			}, 2*time.Minute, 5*time.Second).Should(BeTrue())

			// Verify HPA is also deleted
			Eventually(func() bool {
				var hpa autoscalingv2.HorizontalPodAutoscaler
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name + "-hpa",
					Namespace: namespace,
				}, &hpa)
				return err != nil
			}, 2*time.Minute, 5*time.Second).Should(BeTrue(), "HPA should be deleted with Dragonfly instance")
		}
	}

	AfterAll(func() {
		// Cleanup after all tests are complete
		cleanupResources()
	})

	Context("HPA Resource Creation and Management", func() {
		BeforeEach(func() {
			// Ensure clean state and create Dragonfly instance for each test
			var existingDf resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &existingDf)
			if err == nil {
				fmt.Printf("Found existing Dragonfly resource, deleting it first...\n")
				err = k8sClient.Delete(ctx, &existingDf)
				if err != nil {
					fmt.Printf("Warning: Failed to delete existing resource: %v\n", err)
				}
				// Wait for deletion
				Eventually(func() bool {
					err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &existingDf)
					return err != nil
				}, 30*time.Second, 2*time.Second).Should(BeTrue())
				fmt.Printf("Existing Dragonfly resource deleted\n")
			}

			// Create Dragonfly instance with autoscaler configuration
			df = &resourcesv1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: resourcesv1.DragonflySpec{
					Replicas:        2, // Initial replica count
					Resources:       &resourcesReq,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Autoscaler: &resourcesv1.AutoscalerSpec{
						Enabled:                        true,
						MinReplicas:                    2,
						MaxReplicas:                    5,
						TargetCPUUtilizationPercentage: func() *int32 { v := int32(70); return &v }(),
						Metrics: []autoscalingv2.MetricSpec{
							{
								Type: autoscalingv2.ResourceMetricSourceType,
								Resource: &autoscalingv2.ResourceMetricSource{
									Name: corev1.ResourceCPU,
									Target: autoscalingv2.MetricTarget{
										Type:               autoscalingv2.UtilizationMetricType,
										AverageUtilization: func() *int32 { v := int32(70); return &v }(),
									},
								},
							},
						},
					},
				},
			}

			fmt.Printf("Creating Dragonfly resource: %s/%s\n", namespace, name)
			err = k8sClient.Create(ctx, df)
			Expect(err).To(BeNil())
			fmt.Printf("Dragonfly resource created successfully\n")

			// Check if the Dragonfly resource was actually created
			var createdDf resourcesv1.Dragonfly
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &createdDf)
			Expect(err).To(BeNil())
			fmt.Printf("Dragonfly resource confirmed created with status phase: %s\n", createdDf.Status.Phase)

			// Wait for resources to be created with debugging
			fmt.Printf("Waiting for Dragonfly phase to reach ResourcesCreated...\n")
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseResourcesCreated, 2*time.Minute)
			if err != nil {
				// Get current status for debugging
				k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &createdDf)
				fmt.Printf("Failed to reach ResourcesCreated phase. Current phase: %s, error: %v\n", createdDf.Status.Phase, err)
				Expect(err).To(BeNil(), "Should reach ResourcesCreated phase")
			}
			fmt.Printf("Dragonfly reached ResourcesCreated phase\n")
		})

		It("Should create Dragonfly instance with autoscaler enabled", func() {
			// This test now just verifies that the Dragonfly resource exists and is configured correctly
			var createdDf resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &createdDf)
			Expect(err).To(BeNil())
			Expect(createdDf.Spec.Autoscaler).NotTo(BeNil())
			Expect(createdDf.Spec.Autoscaler.Enabled).To(BeTrue())
			Expect(createdDf.Spec.Autoscaler.MinReplicas).To(Equal(int32(2)))
			Expect(createdDf.Spec.Autoscaler.MaxReplicas).To(Equal(int32(5)))
		})

		It("Should create HPA resource", func() {
			// Check if HPA is created
			var hpa autoscalingv2.HorizontalPodAutoscaler
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name + "-hpa",
				Namespace: namespace,
			}, &hpa)
			Expect(err).To(BeNil())

			// Verify HPA configuration
			Expect(hpa.Spec.ScaleTargetRef.Kind).To(Equal("StatefulSet"))
			Expect(hpa.Spec.ScaleTargetRef.Name).To(Equal(name))
			Expect(*hpa.Spec.MinReplicas).To(Equal(int32(2)))
			Expect(hpa.Spec.MaxReplicas).To(Equal(int32(5)))
			Expect(hpa.Spec.Metrics).To(HaveLen(1))
			Expect(hpa.Spec.Metrics[0].Type).To(Equal(autoscalingv2.ResourceMetricSourceType))
			Expect(hpa.Spec.Metrics[0].Resource.Name).To(Equal(corev1.ResourceCPU))
			Expect(*hpa.Spec.Metrics[0].Resource.Target.AverageUtilization).To(Equal(int32(70)))
		})

		It("Should create StatefulSet with correct initial replica count", func() {
			// With autoscaler enabled, StatefulSet should be created with MinReplicas (2)
			// and then HPA will manage scaling based on metrics
			Eventually(func() error {
				var sts appsv1.StatefulSet
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: namespace,
				}, &sts)
				if err != nil {
					return err
				}

				// Check if StatefulSet has the expected replica count (MinReplicas = 2)
				if sts.Spec.Replicas == nil || *sts.Spec.Replicas != 2 {
					return fmt.Errorf("StatefulSet replicas is %v, expected 2", sts.Spec.Replicas)
				}
				return nil
			}, 60*time.Second, 2*time.Second).Should(Succeed())

			// Final verification
			var sts appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())
			Expect(*sts.Spec.Replicas).To(Equal(int32(2)))
		})

		It("Should wait for all pods to be ready and have correct roles", func() {
			// Wait for StatefulSet to be ready
			err := waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())

			// Wait for Dragonfly to be ready
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)
			Expect(err).To(BeNil())

			// Check pod roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(2))

			// Verify one master and one replica
			masterCount := 0
			replicaCount := 0
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				Expect(ok).To(BeTrue())
				switch role {
				case resources.Master:
					masterCount++
				case resources.Replica:
					replicaCount++
				}
			}
			Expect(masterCount).To(Equal(1))
			Expect(replicaCount).To(Equal(1))
		})
	})

	Context("Replica Count Preservation During Reconciliation", func() {
		It("Should preserve HPA-modified replica count", func() {
			// Simulate HPA scaling by directly modifying the StatefulSet
			var sts appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())

			// Scale up to 3 replicas (simulating HPA action)
			patchFrom := client.MergeFrom(sts.DeepCopy())
			newReplicas := int32(3)
			sts.Spec.Replicas = &newReplicas
			err = k8sClient.Patch(ctx, &sts, patchFrom)
			Expect(err).To(BeNil())

			// Wait for the new pod to be created
			time.Sleep(10 * time.Second)

			// Trigger a reconcile by updating the Dragonfly spec
			var dfUpdate resourcesv1.Dragonfly
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &dfUpdate)
			Expect(err).To(BeNil())

			// Add an annotation to trigger reconcile
			patchFrom = client.MergeFrom(dfUpdate.DeepCopy())
			if dfUpdate.ObjectMeta.Annotations == nil {
				dfUpdate.ObjectMeta.Annotations = make(map[string]string)
			}
			dfUpdate.ObjectMeta.Annotations["test-trigger"] = fmt.Sprintf("%d", time.Now().Unix())
			err = k8sClient.Patch(ctx, &dfUpdate, patchFrom)
			Expect(err).To(BeNil())

			// Wait for reconciliation
			time.Sleep(15 * time.Second)

			// Verify that the replica count is preserved
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())
			Expect(*sts.Spec.Replicas).To(Equal(int32(3)), "StatefulSet replica count should be preserved during reconciliation")
		})

		It("Should configure new pod as replica", func() {
			// Wait for all pods to be ready
			err := waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)
			Expect(err).To(BeNil())

			// Check that we now have 3 pods with correct roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(3))

			// Verify still one master and two replicas
			masterCount := 0
			replicaCount := 0
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				Expect(ok).To(BeTrue())
				switch role {
				case resources.Master:
					masterCount++
				case resources.Replica:
					replicaCount++
				}
			}
			Expect(masterCount).To(Equal(1))
			Expect(replicaCount).To(Equal(2))
		})
	})

	Context("Master Election with Autoscaling", func() {
		It("Should handle master failover when scaled", func() {
			// Get current master
			var pods corev1.PodList
			err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
				resources.RoleLabelKey:             resources.Master,
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(1))

			currentMaster := pods.Items[0]

			// Delete the master pod
			err = k8sClient.Delete(ctx, &currentMaster)
			Expect(err).To(BeNil())

			// Wait for a new master to be elected
			Eventually(func() int {
				var masterPods corev1.PodList
				err := k8sClient.List(ctx, &masterPods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
					resources.RoleLabelKey:             resources.Master,
				})
				if err != nil {
					return 0
				}
				return len(masterPods.Items)
			}, 2*time.Minute, 5*time.Second).Should(Equal(1), "A new master should be elected")

			// Wait for the StatefulSet to be ready again
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())

			// Verify we still have the correct number of pods with correct roles
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(3))
		})

		It("Should handle replica deletion and recreation", func() {
			// Get current replicas
			var replicaPods corev1.PodList
			err := k8sClient.List(ctx, &replicaPods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
				resources.RoleLabelKey:             resources.Replica,
			})
			Expect(err).To(BeNil())
			Expect(replicaPods.Items).To(HaveLen(2))

			// Delete one replica pod
			replicaToDelete := replicaPods.Items[0]
			err = k8sClient.Delete(ctx, &replicaToDelete)
			Expect(err).To(BeNil())

			// Wait for the pod to be recreated and configured as replica
			Eventually(func() int {
				var newReplicaPods corev1.PodList
				err := k8sClient.List(ctx, &newReplicaPods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
					resources.RoleLabelKey:             resources.Replica,
				})
				if err != nil {
					return 0
				}
				return len(newReplicaPods.Items)
			}, 2*time.Minute, 5*time.Second).Should(Equal(2), "Replica pod should be recreated")

			// Wait for StatefulSet to be ready
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())

			// Verify final pod count and roles
			var allPods corev1.PodList
			err = k8sClient.List(ctx, &allPods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(allPods.Items).To(HaveLen(3))

			// Count roles
			masterCount := 0
			replicaCount := 0
			for _, pod := range allPods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				Expect(ok).To(BeTrue())
				switch role {
				case resources.Master:
					masterCount++
				case resources.Replica:
					replicaCount++
				}
			}
			Expect(masterCount).To(Equal(1))
			Expect(replicaCount).To(Equal(2))
		})

		It("Should handle HPA scaling down to minimum replicas", func() {
			// Get current StatefulSet
			var sts appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())

			// Simulate HPA scaling down to minimum (2 replicas)
			patchFrom := client.MergeFrom(sts.DeepCopy())
			minReplicas := int32(2)
			sts.Spec.Replicas = &minReplicas
			err = k8sClient.Patch(ctx, &sts, patchFrom)
			Expect(err).To(BeNil())

			// Wait for pods to be terminated
			Eventually(func() int {
				var pods corev1.PodList
				err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
				})
				if err != nil {
					return -1
				}
				return len(pods.Items)
			}, 2*time.Minute, 5*time.Second).Should(Equal(2), "Should scale down to 2 replicas")

			// Wait for StatefulSet to be ready
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)
			Expect(err).To(BeNil())

			// Verify roles are still correct (1 master, 1 replica)
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(2))

			masterCount := 0
			replicaCount := 0
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				Expect(ok).To(BeTrue())
				switch role {
				case resources.Master:
					masterCount++
				case resources.Replica:
					replicaCount++
				}
			}
			Expect(masterCount).To(Equal(1))
			Expect(replicaCount).To(Equal(1))
		})

		It("Should handle HPA scaling up to maximum replicas", func() {
			// Get current StatefulSet
			var sts appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())

			// Simulate HPA scaling up to maximum (5 replicas)
			patchFrom := client.MergeFrom(sts.DeepCopy())
			maxReplicas := int32(5)
			sts.Spec.Replicas = &maxReplicas
			err = k8sClient.Patch(ctx, &sts, patchFrom)
			Expect(err).To(BeNil())

			// Wait for new pods to be created
			Eventually(func() int {
				var pods corev1.PodList
				err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
				})
				if err != nil {
					return -1
				}
				return len(pods.Items)
			}, 3*time.Minute, 5*time.Second).Should(Equal(5), "Should scale up to 5 replicas")

			// Wait for StatefulSet to be ready
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())

			// Verify roles are correct (1 master, 4 replicas)
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(5))

			masterCount := 0
			replicaCount := 0
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				Expect(ok).To(BeTrue())
				switch role {
				case resources.Master:
					masterCount++
				case resources.Replica:
					replicaCount++
				}
			}
			Expect(masterCount).To(Equal(1))
			Expect(replicaCount).To(Equal(4))
		})
	})

	Context("HPA and Operator Interaction", func() {
		It("Should preserve HPA scaling during operator reconciliation", func() {
			// Ensure we're at 5 replicas from previous test
			var sts appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())
			Expect(*sts.Spec.Replicas).To(Equal(int32(5)))

			// Trigger operator reconciliation by updating Dragonfly spec
			var dfUpdate resourcesv1.Dragonfly
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &dfUpdate)
			Expect(err).To(BeNil())

			// Update a non-scaling related field to trigger reconcile
			patchFrom := client.MergeFrom(dfUpdate.DeepCopy())
			if dfUpdate.ObjectMeta.Annotations == nil {
				dfUpdate.ObjectMeta.Annotations = make(map[string]string)
			}
			dfUpdate.ObjectMeta.Annotations["reconcile-test"] = fmt.Sprintf("%d", time.Now().Unix())
			err = k8sClient.Patch(ctx, &dfUpdate, patchFrom)
			Expect(err).To(BeNil())

			// Wait for reconciliation
			time.Sleep(15 * time.Second)

			// Verify replica count is preserved (not reset to initial value)
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())
			Expect(*sts.Spec.Replicas).To(Equal(int32(5)), "Operator should preserve HPA-managed replica count")
		})

		It("Should handle rapid scaling events", func() {
			// Simulate rapid scaling events (like HPA responding to load)
			var sts appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())

			// Scale down quickly
			patchFrom := client.MergeFrom(sts.DeepCopy())
			replicas := int32(3)
			sts.Spec.Replicas = &replicas
			err = k8sClient.Patch(ctx, &sts, patchFrom)
			Expect(err).To(BeNil())

			// Wait a bit then scale up again
			time.Sleep(5 * time.Second)

			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())

			patchFrom = client.MergeFrom(sts.DeepCopy())
			replicas = int32(4)
			sts.Spec.Replicas = &replicas
			err = k8sClient.Patch(ctx, &sts, patchFrom)
			Expect(err).To(BeNil())

			// Wait for final state to stabilize
			Eventually(func() int {
				var pods corev1.PodList
				err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
				})
				if err != nil {
					return -1
				}
				return len(pods.Items)
			}, 3*time.Minute, 5*time.Second).Should(Equal(4), "Should stabilize at 4 replicas")

			// Wait for StatefulSet to be ready
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())

			// Verify all pods have correct roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(4))

			masterCount := 0
			replicaCount := 0
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				Expect(ok).To(BeTrue())
				switch role {
				case resources.Master:
					masterCount++
				case resources.Replica:
					replicaCount++
				}
			}
			Expect(masterCount).To(Equal(1))
			Expect(replicaCount).To(Equal(3))
		})

	})

	Context("Autoscaler Configuration Updates", func() {
		It("Should update HPA when autoscaler spec changes", func() {
			// Update the autoscaler configuration
			var dfUpdate resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &dfUpdate)
			Expect(err).To(BeNil())

			patchFrom := client.MergeFrom(dfUpdate.DeepCopy())
			dfUpdate.Spec.Autoscaler.MaxReplicas = 8
			dfUpdate.Spec.Autoscaler.TargetCPUUtilizationPercentage = func() *int32 { v := int32(80); return &v }()
			// Update the metrics field as well since it takes precedence over TargetCPUUtilizationPercentage
			dfUpdate.Spec.Autoscaler.Metrics = []autoscalingv2.MetricSpec{
				{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricSource{
						Name: corev1.ResourceCPU,
						Target: autoscalingv2.MetricTarget{
							Type:               autoscalingv2.UtilizationMetricType,
							AverageUtilization: func() *int32 { v := int32(80); return &v }(),
						},
					},
				},
			}
			err = k8sClient.Patch(ctx, &dfUpdate, patchFrom)
			Expect(err).To(BeNil())

			// Wait for reconciliation
			time.Sleep(10 * time.Second)

			// Verify HPA is updated
			var hpa autoscalingv2.HorizontalPodAutoscaler
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name + "-hpa",
				Namespace: namespace,
			}, &hpa)
			Expect(err).To(BeNil())
			Expect(hpa.Spec.MaxReplicas).To(Equal(int32(8)))
			Expect(*hpa.Spec.Metrics[0].Resource.Target.AverageUtilization).To(Equal(int32(80)))
		})

		It("Should disable autoscaler and remove HPA", func() {
			// Disable autoscaler
			var dfUpdate resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &dfUpdate)
			Expect(err).To(BeNil())

			patchFrom := client.MergeFrom(dfUpdate.DeepCopy())
			dfUpdate.Spec.Autoscaler.Enabled = false
			err = k8sClient.Patch(ctx, &dfUpdate, patchFrom)
			Expect(err).To(BeNil())

			// Verify the patch worked by checking the updated resource
			Eventually(func() bool {
				var df resourcesv1.Dragonfly
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: namespace,
				}, &df)
				if err != nil {
					return false
				}
				return df.Spec.Autoscaler != nil && !df.Spec.Autoscaler.Enabled
			}, 10*time.Second, 1*time.Second).Should(BeTrue(), "Dragonfly autoscaler should be disabled")

			// Wait for reconciliation and HPA deletion
			Eventually(func() error {
				var hpa autoscalingv2.HorizontalPodAutoscaler
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      name + "-hpa",
					Namespace: namespace,
				}, &hpa)
			}, 60*time.Second, 2*time.Second).ShouldNot(BeNil(), "HPA should be deleted when autoscaler is disabled")

			// Final verification that HPA is removed
			var hpa autoscalingv2.HorizontalPodAutoscaler
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name + "-hpa",
				Namespace: namespace,
			}, &hpa)
			Expect(err).ToNot(BeNil(), "HPA should be deleted when autoscaler is disabled")
		})
	})

	Context("Custom Metrics and Behavior", func() {
		It("Should support custom metrics configuration", func() {
			// Re-enable autoscaler with custom metrics
			var dfUpdate resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &dfUpdate)
			Expect(err).To(BeNil())

			patchFrom := client.MergeFrom(dfUpdate.DeepCopy())
			dfUpdate.Spec.Autoscaler.Enabled = true
			dfUpdate.Spec.Autoscaler.MinReplicas = 2
			dfUpdate.Spec.Autoscaler.MaxReplicas = 6
			dfUpdate.Spec.Autoscaler.Metrics = []autoscalingv2.MetricSpec{
				{
					Type: autoscalingv2.ResourceMetricSourceType,
					Resource: &autoscalingv2.ResourceMetricSource{
						Name: corev1.ResourceCPU,
						Target: autoscalingv2.MetricTarget{
							Type:               autoscalingv2.UtilizationMetricType,
							AverageUtilization: func() *int32 { v := int32(60); return &v }(),
						},
					},
				},
			}

			// Add scaling behavior
			dfUpdate.Spec.Autoscaler.Behavior = &autoscalingv2.HorizontalPodAutoscalerBehavior{
				ScaleDown: &autoscalingv2.HPAScalingRules{
					StabilizationWindowSeconds: func() *int32 { v := int32(300); return &v }(),
					Policies: []autoscalingv2.HPAScalingPolicy{
						{
							Type:          autoscalingv2.PercentScalingPolicy,
							Value:         10,
							PeriodSeconds: 60,
						},
					},
				},
				ScaleUp: &autoscalingv2.HPAScalingRules{
					StabilizationWindowSeconds: func() *int32 { v := int32(60); return &v }(),
					Policies: []autoscalingv2.HPAScalingPolicy{
						{
							Type:          autoscalingv2.PodsScalingPolicy,
							Value:         2,
							PeriodSeconds: 60,
						},
					},
				},
			}

			err = k8sClient.Patch(ctx, &dfUpdate, patchFrom)
			Expect(err).To(BeNil())

			// Wait for reconciliation
			time.Sleep(10 * time.Second)

			// Verify HPA is recreated with new configuration
			var hpa autoscalingv2.HorizontalPodAutoscaler
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name + "-hpa",
				Namespace: namespace,
			}, &hpa)
			Expect(err).To(BeNil())
			Expect(hpa.Spec.MaxReplicas).To(Equal(int32(6)))
			Expect(hpa.Spec.Metrics).To(HaveLen(1))

			// Check CPU metric
			cpuMetricFound := false
			for _, metric := range hpa.Spec.Metrics {
				if metric.Resource.Name == corev1.ResourceCPU {
					Expect(*metric.Resource.Target.AverageUtilization).To(Equal(int32(60)))
					cpuMetricFound = true
				}
			}
			Expect(cpuMetricFound).To(BeTrue())

			// Check behavior configuration
			Expect(hpa.Spec.Behavior).ToNot(BeNil())
			Expect(hpa.Spec.Behavior.ScaleDown).ToNot(BeNil())
			Expect(*hpa.Spec.Behavior.ScaleDown.StabilizationWindowSeconds).To(Equal(int32(300)))
			Expect(hpa.Spec.Behavior.ScaleUp).ToNot(BeNil())
			Expect(*hpa.Spec.Behavior.ScaleUp.StabilizationWindowSeconds).To(Equal(int32(60)))
		})
	})

	Context("Edge Cases and Failure Scenarios", func() {
		It("Should handle multiple concurrent pod deletions", func() {
			// Scale up to have more pods to work with
			var sts appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &sts)
			Expect(err).To(BeNil())

			patchFrom := client.MergeFrom(sts.DeepCopy())
			replicas := int32(4)
			sts.Spec.Replicas = &replicas
			err = k8sClient.Patch(ctx, &sts, patchFrom)
			Expect(err).To(BeNil())

			// Wait for scale up
			Eventually(func() int {
				var pods corev1.PodList
				err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
				})
				if err != nil {
					return -1
				}
				return len(pods.Items)
			}, 2*time.Minute, 5*time.Second).Should(Equal(4))

			// Get all pods
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pods.Items).To(HaveLen(4))

			// Delete multiple pods simultaneously (but not all)
			var podsToDelete []corev1.Pod
			for i, pod := range pods.Items {
				if i < 2 { // Delete 2 pods
					podsToDelete = append(podsToDelete, pod)
				}
			}

			// Delete pods concurrently
			for _, pod := range podsToDelete {
				go func(p corev1.Pod) {
					k8sClient.Delete(ctx, &p)
				}(pod)
			}

			// Wait for pods to be recreated and cluster to stabilize
			Eventually(func() bool {
				var newPods corev1.PodList
				err := k8sClient.List(ctx, &newPods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
				})
				if err != nil || len(newPods.Items) != 4 {
					return false
				}

				// Check that all pods are running and have roles assigned
				masterCount := 0
				replicaCount := 0
				allRunning := true
				for _, pod := range newPods.Items {
					if pod.Status.Phase != corev1.PodRunning {
						allRunning = false
						break
					}
					role, hasRole := pod.Labels[resources.RoleLabelKey]
					if !hasRole {
						return false
					}
					switch role {
					case resources.Master:
						masterCount++
					case resources.Replica:
						replicaCount++
					}
				}
				return allRunning && masterCount == 1 && replicaCount == 3
			}, 3*time.Minute, 10*time.Second).Should(BeTrue(), "Should recover from multiple pod deletions")
		})
	})
})
