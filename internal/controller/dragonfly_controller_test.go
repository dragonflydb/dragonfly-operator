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
	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Dragonfly Reconciler", func() {

	ctx := context.Background()
	name := "df-test"
	namespace := "default"
	df := dragonflydbiov1alpha1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: dragonflydbiov1alpha1.DragonflySpec{
			Replicas: 3,
			Image:    fmt.Sprintf("%s:%s", resources.DragonflyImage, "latest"),
		},
	}

	Context("Dragonfly resource creation", func() {
		It("Should create successfully", func() {

			err := k8sClient.Create(ctx, &df)
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

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 4 pod replicas = 1 master + 3 replicas
			Expect(pods.Items).To(HaveLen(4))
		})

		It("Update to replicas should be propagated successfully", func() {
			// Update df to the latest
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			df.Spec.Replicas = 2
			err = k8sClient.Update(ctx, &df)
			Expect(err).To(BeNil())

			// Wait until Dragonfly object is marked resources-created
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, PhaseReady, 2*time.Minute)
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

			// 3 pod replicas = 1 master + 2 replicas
			Expect(pods.Items).To(HaveLen(3))
		})

		It("Update to image should be propagated successfully", func() {
			newImage := resources.DragonflyImage + ":v0.17.0"
			// Update df to the latest
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			df.Spec.Image = newImage
			err = k8sClient.Update(ctx, &df)
			Expect(err).To(BeNil())

			// Wait until Dragonfly object is marked resources-created
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, PhaseReady, 3*time.Minute)
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

		})
	})
})

func waitForDragonflyPhase(ctx context.Context, c client.Client, name, namespace, phase string, maxDuration time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for statefulset to be ready")
		default:
			// Check if the statefulset is ready
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

	if df.Status.Phase == phase {
		return true, nil
	}

	return false, nil
}
