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
	"time"

	dragonflydbiov1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Health Reconciler", func() {
	ctx := context.Background()
	podRoles := map[string][]string{
		resources.Master:  make([]string, 0),
		resources.Replica: make([]string, 0),
	}
	name := "health-test"
	namespace := "default"
	replicas := 3

	Context("Fail Over is working", func() {
		It("Initial Master is elected", func() {
			err := k8sClient.Create(ctx, &dragonflydbiov1alpha1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: dragonflydbiov1alpha1.DragonflySpec{
					Replicas: int32(replicas),
					Image:    fmt.Sprintf("%s:%s", resources.DragonflyImage, "latest"),
				},
			})
			Expect(err).To(BeNil())

			// Wait until Dragonfly object is marked initialized
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, PhaseResourcesCreated, 2*time.Minute)
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

			waitForDragonflyPhase(ctx, k8sClient, name, namespace, PhaseReady, 2*time.Minute)

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 4 pod replicas = 1 master + 3 replicas
			Expect(pods.Items).To(HaveLen(replicas + 1))

			// Get the pods along with their roles
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.Role]
				// error if there is no label
				Expect(ok).To(BeTrue())

				podRoles[role] = append(podRoles[role], pod.Name)
			}

			// One Master & Three Replicas
			Expect(podRoles[resources.Master]).To(HaveLen(1))
			Expect(podRoles[resources.Replica]).To(HaveLen(replicas))
		})

		It("New Master is elected as old one dies", func() {
			// Get & Delete the old master
			var pod corev1.Pod
			err := k8sClient.Get(ctx, types.NamespacedName{
				Namespace: namespace,
				Name:      podRoles[resources.Master][0],
			}, &pod)
			Expect(err).To(BeNil())

			err = k8sClient.Delete(ctx, &pod)
			Expect(err).To(BeNil())

			// Expect a new master along while having 3 replicas
			// Wait for Status to be ready
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, PhaseReady, 2*time.Minute)

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 4 pod replicas = 1 master + 3 replicas
			Expect(pods.Items).To(HaveLen(replicas + 1))

			// Get the pods along with their roles
			podRoles := make(map[string][]string)
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.Role]
				// error if there is no label
				Expect(ok).To(BeTrue())
				// verify the role to match the label
				podRoles[role] = append(podRoles[role], pod.Name)
			}

			// One Master & Three Replicas
			Expect(podRoles[resources.Master]).To(HaveLen(1))
			Expect(podRoles[resources.Replica]).To(HaveLen(replicas))
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

			// Expect a new replica
			// Wait for Status to be ready
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, PhaseReady, 2*time.Minute)

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 4 pod replicas = 1 master + 3 replicas
			Expect(pods.Items).To(HaveLen(replicas + 1))

			// Get the pods along with their roles
			podRoles := make(map[string][]string)
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.Role]
				// error if there is no label
				Expect(ok).To(BeTrue())
				// verify the role to match the label
				podRoles[role] = append(podRoles[role], pod.Name)
			}

			// One Master & Three Replicas
			Expect(podRoles[resources.Master]).To(HaveLen(1))
			Expect(podRoles[resources.Replica]).To(HaveLen(replicas))
		})
	})
})
