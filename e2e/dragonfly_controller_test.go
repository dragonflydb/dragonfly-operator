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

	dragonflydbiov1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/controller"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Dragonfly Reconciler", Ordered, func() {
	ctx := context.Background()
	name := "df-test"
	namespace := "default"
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

	args := []string{
		"--vmodule=replica=1,server_family=1",
	}

	df := dragonflydbiov1alpha1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: dragonflydbiov1alpha1.DragonflySpec{
			Replicas:  3,
			Image:     fmt.Sprintf("%s:%s", resources.DragonflyImage, "latest"),
			Resources: &resourcesReq,
			Args:      args,
		},
	}

	Context("Dragonfly resource creation", func() {
		It("Should create successfully", func() {
			err := k8sClient.Create(ctx, &df)
			Expect(err).To(BeNil())

			// Wait until Dragonfly object is marked initialized
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseResourcesCreated, 2*time.Minute)
			waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)

			// Check for service and statefulset
			var ss appsv1.StatefulSet
			err = k8sClient.Get(ctx, types.NamespacedName{
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

			// check resource requirements of statefulset
			Expect(ss.Spec.Template.Spec.Containers[0].Resources).To(Equal(*df.Spec.Resources))
			// check args of statefulset
			Expect(ss.Spec.Template.Spec.Containers[0].Args[1:]).To(Equal(df.Spec.Args))

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 3 pod replicas = 1 master + 2 replicas
			Expect(pods.Items).To(HaveLen(3))

			// check for pod resources
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU].Equal(resourcesReq.Limits[corev1.ResourceCPU])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory].Equal(resourcesReq.Limits[corev1.ResourceMemory])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU].Equal(resourcesReq.Requests[corev1.ResourceCPU])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory].Equal(resourcesReq.Requests[corev1.ResourceMemory])).To(BeTrue())
		})

		It("Increase in replicas should be propagated successfully", func() {
			// Update df to the latest
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			df.Spec.Replicas = 5
			err = k8sClient.Update(ctx, &df)
			Expect(err).To(BeNil())

			// Wait until Dragonfly object is marked resources-created
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)
			waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)

			// Check for service and statefulset
			var ss appsv1.StatefulSet
			err = k8sClient.Get(ctx, types.NamespacedName{
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

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 5 pod replicas = 1 master + 4 replicas
			Expect(pods.Items).To(HaveLen(5))
		})

		It("Decrease to replicas should be propagated successfully", func() {
			// Update df to the latest
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			df.Spec.Replicas = 3
			err = k8sClient.Update(ctx, &df)
			Expect(err).To(BeNil())

			// Wait until Dragonfly object is marked resources-created
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)
			waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)

			time.Sleep(40 * time.Second)

			// Check for service and statefulset
			var ss appsv1.StatefulSet
			err = k8sClient.Get(ctx, types.NamespacedName{
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

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 3 pod replicas = 1 master + 2 replicas
			Expect(pods.Items).To(HaveLen(3))

			// Get the pods along with their roles
			podRoles := make(map[string][]string)
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.Role]
				// error if there is no label
				Expect(ok).To(BeTrue())
				// verify the role to match the label
				podRoles[role] = append(podRoles[role], pod.Name)
			}

			// One Master & Two Replicas
			Expect(podRoles[resources.Master]).To(HaveLen(1))
			Expect(podRoles[resources.Replica]).To(HaveLen(2))
		})

		It("Update to image should be propagated successfully", func() {
			newImage := resources.DragonflyImage + ":v1.1.0"
			// Update df to the latest
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			df.Spec.Image = newImage
			err = k8sClient.Update(ctx, &df)
			Expect(err).To(BeNil())

			time.Sleep(30 * time.Second)

			// Wait until Dragonfly object is marked resources-created
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())

			// Check for service and statefulset
			var ss appsv1.StatefulSet
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &ss)
			Expect(err).To(BeNil())

			// check for pod image
			Expect(ss.Spec.Template.Spec.Containers[0].Image).To(Equal(newImage))

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// Get the pods along with their roles
			podRoles := make(map[string][]string)
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.Role]
				// error if there is no label
				Expect(ok).To(BeTrue())
				// verify the role to match the label
				podRoles[role] = append(podRoles[role], pod.Name)
			}

			// One Master & Two Replicas
			Expect(podRoles[resources.Master]).To(HaveLen(1))
			Expect(podRoles[resources.Replica]).To(HaveLen(2))

		})

		It("Update to resources and args should be propagated successfully", func() {
			newResources := corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("0.5"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			}

			newArgs := []string{
				"--vmodule=replica=1",
			}

			newAnnotations := map[string]string{
				"foo": "bar",
			}

			newTolerations := []corev1.Toleration{
				{
					Key:      "foo",
					Operator: corev1.TolerationOpEqual,
					Value:    "bar",
					Effect:   corev1.TaintEffectPreferNoSchedule,
				},
			}

			newAffinity := corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
						{
							Weight: 1,
							Preference: corev1.NodeSelectorTerm{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "foo",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"bar"},
									},
								},
							},
						},
					},
				},
			}

			// Update df to the latest
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			df.Spec.Resources = &newResources
			df.Spec.Args = newArgs
			df.Spec.Tolerations = newTolerations
			df.Spec.Affinity = &newAffinity
			df.Spec.Annotations = newAnnotations

			err = k8sClient.Update(ctx, &df)
			Expect(err).To(BeNil())

			// Wait until Dragonfly object is marked resources-created
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())

			// Check for service and statefulset
			var ss appsv1.StatefulSet
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &ss)
			Expect(err).To(BeNil())

			// check for pod args
			Expect(ss.Spec.Template.Spec.Containers[0].Args[1:]).To(Equal(newArgs))

			// check for pod resources
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU].Equal(newResources.Limits[corev1.ResourceCPU])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory].Equal(newResources.Limits[corev1.ResourceMemory])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU].Equal(newResources.Requests[corev1.ResourceCPU])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory].Equal(newResources.Requests[corev1.ResourceMemory])).To(BeTrue())

			// check for annotations
			Expect(ss.Spec.Template.ObjectMeta.Annotations).To(Equal(newAnnotations))

			// check for tolerations
			Expect(ss.Spec.Template.Spec.Tolerations).To(Equal(newTolerations))

			// check for affinity
			Expect(ss.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution).To(Equal(newAffinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution))
		})
	})
})

func waitForDragonflyPhase(ctx context.Context, c client.Client, name, namespace, phase string, maxDuration time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for Dragonfly to be ready")
		default:
			// Check if the dragonfly is ready
			ready, err := isDragonflyInphase(ctx, c, name, namespace, phase)
			if err != nil {
				return err
			}
			if ready {
				return nil
			}
		}
	}
}

func isDragonflyInphase(ctx context.Context, c client.Client, name, namespace, phase string) (bool, error) {
	var df resourcesv1.Dragonfly
	if err := c.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, &df); err != nil {
		return false, nil
	}

	// Ready means we also want rolling update to be false
	if phase == controller.PhaseReady {
		// check for replicas
		if df.Status.IsRollingUpdate {
			return false, nil
		}
	}

	if df.Status.Phase == phase {
		return true, nil
	}

	return false, nil
}
