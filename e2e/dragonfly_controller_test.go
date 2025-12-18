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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/controller"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Dragonfly Lifecycle tests", Ordered, FlakeAttempts(3), func() {
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

	customLabelName := "a.custom/label"
	customLabelValue := "my-value"

	labels := map[string]string{
		customLabelName: customLabelValue,
	}

	df := resourcesv1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: resourcesv1.DragonflySpec{
			OwnedObjectsMetadata: &resourcesv1.OwnedObjectsMetadata{
				Labels: labels,
			},
			Replicas:  3,
			Resources: &resourcesReq,
			Args:      args,
			Env: []corev1.EnvVar{
				{
					Name:  "ENV-1",
					Value: "value-1",
				},
			},
			Authentication: &resourcesv1.Authentication{
				PasswordFromSecret: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "df-secret",
					},
					Key: "password",
				},
			},
			Affinity: &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
						{
							Weight: 1,
							Preference: corev1.NodeSelectorTerm{
								MatchExpressions: []corev1.NodeSelectorRequirement{
									{
										Key:      "database",
										Operator: corev1.NodeSelectorOpIn,
										Values:   []string{"dragonfly"},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	Context("Dragonfly resource creation", func() {
		password := "df-pass-1"
		It("Should create successfully", func() {
			// create the secret
			err := k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "df-secret",
					Namespace: namespace,
				},
				StringData: map[string]string{
					"password": password,
				},
			})
			Expect(err).To(BeNil())
		})

		It("Should create successfully", func() {
			err := k8sClient.Create(ctx, &df)
			Expect(err).To(BeNil())

			// Wait until Dragonfly object is marked initialized
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseResourcesCreated, 2*time.Minute)
			waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)

		})

		var ss appsv1.StatefulSet
		It("Check for values in statefulset", func() {
			// Check for service and statefulset
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

			// check labels of statefulset
			Expect(ss.Labels[customLabelName]).To(Equal(df.Spec.OwnedObjectsMetadata.Labels[customLabelName]))
			Expect(svc.Labels[customLabelName]).To(Equal(df.Spec.OwnedObjectsMetadata.Labels[customLabelName]))

			// check resource requirements of statefulset
			Expect(ss.Spec.Template.Spec.Containers[0].Resources).To(Equal(*df.Spec.Resources))
			// check args of statefulset
			expectArgs := append(resources.DefaultDragonflyArgs, df.Spec.Args...)
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElements(expectArgs))

			// check for pod resources
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU].Equal(resourcesReq.Limits[corev1.ResourceCPU])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory].Equal(resourcesReq.Limits[corev1.ResourceMemory])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU].Equal(resourcesReq.Requests[corev1.ResourceCPU])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory].Equal(resourcesReq.Requests[corev1.ResourceMemory])).To(BeTrue())

			// check for affinity
			Expect(ss.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution).To(Equal(df.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution))

			// check for env
			Expect(ss.Spec.Template.Spec.Containers[0].Env).To(ContainElements(df.Spec.Env))

			// Authentication
			// PasswordFromSecret
			Expect(ss.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{
				Name: "DFLY_requirepass",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: df.Spec.Authentication.PasswordFromSecret,
				},
			}))
		})

		It("Check for pod values", func() {
			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 3 pod replicas = 1 master + 2 replicas
			Expect(pods.Items).To(HaveLen(3))

		})

		It("Check for connectivity and insert data", func() {
			stopChan := make(chan struct{}, 1)
			rc, err := checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, password, 6391)
			Expect(err).To(BeNil())
			defer close(stopChan)
			defer rc.Close()

			// insert test data
			Expect(rc.Set(ctx, "foo", "bar", 0).Err()).To(BeNil())
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
				resources.DragonflyNameLabelKey:    name,
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

			// Wait until Dragonfly object is marked ready
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
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 3 pod replicas = 1 master + 2 replicas
			Expect(pods.Items).To(HaveLen(3))

			// Get the pods along with their roles
			podRoles := make(map[string][]string)
			for _, pod := range pods.Items {
				role, ok := pod.Labels[resources.RoleLabelKey]
				// error if there is no label
				Expect(ok).To(BeTrue())
				// verify the role to match the label
				podRoles[role] = append(podRoles[role], pod.Name)
			}

			// One Master & Two Replicas
			Expect(podRoles[resources.Master]).To(HaveLen(1))
			Expect(podRoles[resources.Replica]).To(HaveLen(2))
		})

		It("Updates should be propagated successfully", func() {
			// Update df to the latest
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			df.Spec.Image = fmt.Sprintf("%s:%s", resources.DragonflyImage, "v1.30.3")
			err = k8sClient.Update(ctx, &df)
			Expect(err).To(BeNil())
		})

		It("Check for values in statefulset", func() {
			// Wait until Dragonfly object is marked ready
			err := waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)
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

			// check for image
			Expect(ss.Spec.Template.Spec.Containers[0].Image).To(Equal(df.Spec.Image))

			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// Get the pods along with their roles
			podRoles := make(map[string][]string)
			for _, pod := range pods.Items {
				Expect(pod.Spec.Containers[0].Image).To(Equal(df.Spec.Image))
				role, ok := pod.Labels[resources.RoleLabelKey]
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

			GinkgoLogr.Info("start timestamp", "timestamp", time.Now().UTC())
			// Wait until Dragonfly object is marked ready
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())
			GinkgoLogr.Info("end timestamp", "timestamp", time.Now().UTC())

			// Check for service and statefulset
			var ss appsv1.StatefulSet
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &ss)
			Expect(err).To(BeNil())

			// check for pod args
			expectedArgs := append(resources.DefaultDragonflyArgs, newArgs...)
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElements(expectedArgs))

			// check for pod resources
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU].Equal(newResources.Limits[corev1.ResourceCPU])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory].Equal(newResources.Limits[corev1.ResourceMemory])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU].Equal(newResources.Requests[corev1.ResourceCPU])).To(BeTrue())
			Expect(ss.Spec.Template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory].Equal(newResources.Requests[corev1.ResourceMemory])).To(BeTrue())

			// check for annotations (allow additional operator-managed keys)
			Expect(ss.Spec.Template.ObjectMeta.Annotations).To(HaveKeyWithValue("foo", "bar"))

			// check for tolerations
			Expect(ss.Spec.Template.Spec.Tolerations).To(Equal(newTolerations))

			// check for affinity
			Expect(ss.Spec.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution).To(Equal(newAffinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution))

			// check for pods too
			var pods corev1.PodList
			err = k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			for _, pod := range pods.Items {
				// check for pod args
				Expect(pod.Spec.Containers[0].Args).To(Equal(expectedArgs))

				// check for pod resources
				Expect(pod.Spec.Containers[0].Resources.Limits[corev1.ResourceCPU].Equal(newResources.Limits[corev1.ResourceCPU])).To(BeTrue())
				Expect(pod.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory].Equal(newResources.Limits[corev1.ResourceMemory])).To(BeTrue())
				Expect(pod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU].Equal(newResources.Requests[corev1.ResourceCPU])).To(BeTrue())
				Expect(pod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory].Equal(newResources.Requests[corev1.ResourceMemory])).To(BeTrue())

				// check for annotations (allow additional operator-managed keys)
				Expect(pod.ObjectMeta.Annotations).To(HaveKeyWithValue("foo", "bar"))

				// check for tolerations
				Expect(pod.Spec.Tolerations).To(ContainElements(newTolerations))

				// check for affinity
				Expect(pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution).To(Equal(newAffinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution))
			}
			// Update df to the latest
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())
			GinkgoLogr.Info("df arg propagate phase", "phase", df.Status.Phase, "rolling-update", df.Status.IsRollingUpdate)

		})

		It("Check for data", func() {
			stopChan := make(chan struct{}, 1)
			defer close(stopChan)
			rc, err := checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, password, 6395)
			Expect(err).To(BeNil())
			defer rc.Close()

			// Check for test data
			data, err := rc.Get(ctx, "foo").Result()
			Expect(err).To(BeNil())
			Expect(data).To(Equal("bar"))
		})

		It("Change Service specification to LoadBalancer", func() {
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())

			newAnnotations := map[string]string{
				"service-annotaions": "true",
			}
			newLabels := map[string]string{
				"service-labels": "true",
			}
			df.Spec.ServiceSpec = &resourcesv1.ServiceSpec{
				Type:        corev1.ServiceTypeLoadBalancer,
				Name:        "test-svc",
				Annotations: newAnnotations,
				Labels:      newLabels,
			}

			GinkgoLogr.Info("df phase", "phase", df.Status.Phase, "rolling-update", df.Status.IsRollingUpdate)
			err = k8sClient.Update(ctx, &df)
			Expect(err).To(BeNil())

			// Wait until Dragonfly object is marked ready
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 1*time.Minute)
			Expect(err).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 3*time.Minute)
			Expect(err).To(BeNil())

			var svc corev1.Service
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      "test-svc",
				Namespace: namespace,
			}, &svc)
			Expect(err).To(BeNil())

			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer))
			Expect(svc.Annotations).To(Equal(newAnnotations))
			Expect(svc.Labels).To(Equal(newLabels))
		})

		It("Should recreate missing statefulset", func() {
			var ss appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &ss)
			Expect(err).To(BeNil())

			Expect(k8sClient.Delete(ctx, &ss)).To(BeNil())
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)
			Expect(err).To(BeNil())
		})

		It("Cleanup", func() {
			var df resourcesv1.Dragonfly
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

var _ = Describe("Dragonfly Acl file secret key test", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "df-acl"
	namespace := "default"

	args := []string{
		"--vmodule=replica=1,server_family=1",
	}

	Context("Dragonfly resource creation with acl file", func() {
		It("Should create successfully", func() {
			multiLineString := `user default on nopass ~* +@all
user john on >peacepass -@all +@string +hset
`

			err := k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "df-acl",
					Namespace: namespace,
				},
				StringData: map[string]string{
					"df-acl": multiLineString,
				},
			})
			Expect(err).To(BeNil())
			err = k8sClient.Create(ctx, &resourcesv1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: resourcesv1.DragonflySpec{
					Replicas: 1,
					Args:     args,
					AclFromSecret: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: "df-acl",
						},
						Key: "df-acl",
					},
				},
			})
			Expect(err).To(BeNil())
		})
		It("Resource should exist", func() {
			// Wait until Dragonfly object is marked initialized
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseResourcesCreated, 2*time.Minute)
			waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)

			// Check for statefulset
			var ss appsv1.StatefulSet
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &ss)
			Expect(err).To(BeNil())

			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement(fmt.Sprintf("--aclfile=%s/dragonfly.acl", resources.AclDir)))
			stopChan := make(chan struct{}, 1)
			defer close(stopChan)
			rc, err := checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6392)
			Expect(err).To(BeNil())
			defer rc.Close()
			result, err := rc.Do(ctx, "acl", "list").StringSlice()
			Expect(err).To(BeNil())
			Expect(result).To(HaveLen(2))
			Expect(result).To(ContainElements(
				"user default on nopass ~* resetchannels +@all $all",
				"user john on #f00a64155eeebd5 resetchannels -@all +@string +hset $all",
			))
		})
		It("Cleanup", func() {
			var df resourcesv1.Dragonfly
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

var _ = Describe("Dragonfly tiering test with single replica", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "df-tier"
	namespace := "default"

	Context("Dragonfly resource creation and data insertion", func() {
		It("Should create successfully", func() {
			err := k8sClient.Create(ctx, &resourcesv1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: resourcesv1.DragonflySpec{
					Replicas: 1,
					Tiering: &resourcesv1.Tiering{
						PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{
								corev1.ReadWriteOnce,
							},
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse("2Gi"),
								},
							},
						},
					},
				},
			})
			Expect(err).To(BeNil())
		})

		It("Resources should exist", func() {
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

			// check if the pvc is created
			var pvcs corev1.PersistentVolumeClaimList
			err = k8sClient.List(ctx, &pvcs, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pvcs.Items).To(HaveLen(1))
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--tiered_prefix=/dragonfly/tiering/vol"))

			// Insert Data
			stopChan := make(chan struct{}, 1)
			rc, err := checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6393)
			Expect(err).To(BeNil())
			defer close(stopChan)
			defer rc.Close()

			// Insert BIG value (>64B so it is eligible for tiering)
			const size = 1 << 20 // 1 MiB
			payload := make([]byte, size)
			_, _ = rand.Read(payload)

			infoStr, err := rc.Info(ctx, "tiered").Result()
			Expect(err).To(BeNil())

			entries, err := parseTieredEntriesFromInfo(infoStr)
			Expect(err).To(BeNil())
			Expect(entries).To(Equal(int64(0))) // make sure this matches your expectation

			Expect(rc.Set(ctx, "foo", payload, 0).Err()).To(BeNil())

			// Inserted one big key, tiered entries should be 1
			infoStr, err = rc.Info(ctx, "tiered").Result()
			Expect(err).To(BeNil())

			fmt.Println("Tiered entried Info: ", infoStr)
			entries, err = parseTieredEntriesFromInfo(infoStr)
			Expect(err).To(BeNil())
			Expect(entries).To(Equal(int64(1))) // make sure this matches your expectation

			// Fetch and compare by size
			data, err := rc.Get(ctx, "foo").Bytes()
			Expect(err).To(BeNil())
			Expect(len(data)).To(Equal(size))
		})

		It("Cleanup", func() {
			var df resourcesv1.Dragonfly
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

var _ = Describe("Dragonfly PVC Test with single replica", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "df-pvc"
	namespace := "default"
	schedule := "*/1 * * * *"

	args := []string{
		"--vmodule=replica=1,server_family=1",
	}

	Context("Dragonfly resource creation and data insertion", func() {
		It("Should create successfully", func() {
			err := k8sClient.Create(ctx, &resourcesv1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: resourcesv1.DragonflySpec{
					Replicas: 1,
					Args:     args,
					Snapshot: &resourcesv1.Snapshot{
						Cron: schedule,
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
		})

		It("Resources should exist", func() {
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

			// check if the pvc is created
			var pvcs corev1.PersistentVolumeClaimList
			err = k8sClient.List(ctx, &pvcs, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pvcs.Items).To(HaveLen(1))
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement(fmt.Sprintf("--snapshot_cron=%s", schedule)))

			// Insert Data
			stopChan := make(chan struct{}, 1)
			rc, err := checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6393)
			Expect(err).To(BeNil())

			// Insert test data
			Expect(rc.Set(ctx, "foo", "bar", 0).Err()).To(BeNil())
			close(stopChan)
			rc.Close()

			// delete the single replica
			var pod corev1.Pod
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-0", name),
				Namespace: namespace,
			}, &pod)
			Expect(err).To(BeNil())

			err = k8sClient.Delete(ctx, &pod)
			Expect(err).To(BeNil())

			time.Sleep(10 * time.Second)

			// Wait until Dragonfly object is marked initialized
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)
			waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)
			// check if the pod is created
			err = k8sClient.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("%s-0", name),
				Namespace: namespace,
			}, &pod)
			Expect(err).To(BeNil())

			// recreate Redis Client on the new pod
			stopChan = make(chan struct{}, 1)
			rc, err = checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6394)
			defer close(stopChan)
			defer rc.Close()
			Expect(err).To(BeNil())

			// check if the Data exists
			data, err := rc.Get(ctx, "foo").Result()
			Expect(err).To(BeNil())
			Expect(data).To(Equal("bar"))
		})

		It("Cleanup", func() {
			var df resourcesv1.Dragonfly
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

var _ = Describe("Dragonfly with additional container and volume", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	const (
		name      = "df-addons"
		namespace = "default"

		sidecarName   = "helper"
		sidecarImage  = "busybox:1.36"
		volumeName    = "extra-storage"
		volumeMountPt = "/opt/data"
	)

	Context("Create DF and verify additional resources are merged", func() {
		It("Should create Dragonfly with an extra container and volume", func() {
			df := &resourcesv1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: resourcesv1.DragonflySpec{
					Replicas: 1,
					AdditionalContainers: []corev1.Container{
						{
							Name:    sidecarName,
							Image:   sidecarImage,
							Command: []string{"sh", "-c", "sleep 3600"},
							VolumeMounts: []corev1.VolumeMount{
								{Name: volumeName, MountPath: volumeMountPt},
							},
						},
					},
					AdditionalVolumes: []corev1.Volume{
						{
							Name: volumeName,
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, df)).To(Succeed())
		})

		It("Resources should include the additional container and volume", func() {
			// Wait for reconciliation to create SS and mark DF resources created
			waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseResourcesCreated, 2*time.Minute)
			waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)

			// Fetch StatefulSet
			var ss appsv1.StatefulSet
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &ss)).To(Succeed())

			// --- Verify additional container exists
			containers := ss.Spec.Template.Spec.Containers
			var helperIdx = -1
			containerNames := make([]string, 0, len(containers))
			for i, c := range containers {
				containerNames = append(containerNames, c.Name)
				if c.Name == sidecarName {
					helperIdx = i
				}
			}
			By("Listing containers: " + strings.Join(containerNames, ", "))
			Expect(helperIdx).To(BeNumerically(">=", 0), "expected sidecar container %q to exist", sidecarName)

			helper := containers[helperIdx]
			Expect(helper.Image).To(Equal(sidecarImage))
			// Check the volume mount by name + path (avoid brittle deep equality)
			var hasMount bool
			for _, vm := range helper.VolumeMounts {
				if vm.Name == volumeName && vm.MountPath == volumeMountPt {
					hasMount = true
					break
				}
			}
			Expect(hasMount).To(BeTrue(), "expected sidecar to mount %q at %q", volumeName, volumeMountPt)

			// --- Verify additional volume exists
			vols := ss.Spec.Template.Spec.Volumes
			var volIdx = -1
			volNames := make([]string, 0, len(vols))
			for i, v := range vols {
				volNames = append(volNames, v.Name)
				if v.Name == volumeName {
					volIdx = i
				}
			}
			By("Listing volumes: " + strings.Join(volNames, ", "))
			Expect(volIdx).To(BeNumerically(">=", 0), "expected volume %q to exist", volumeName)

			Expect(vols[volIdx].EmptyDir).ToNot(BeNil(), "expected %q to be an EmptyDir", volumeName)
		})

		It("Cleanup", func() {
			var df resourcesv1.Dragonfly
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &df)).To(Succeed())
		})
	})
})

var _ = Describe("Dragonfly Server TLS tests", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "df-tls"
	namespace := "default"

	args := []string{
		"--vmodule=replica=1,server_family=1",
	}

	df := resourcesv1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: resourcesv1.DragonflySpec{
			Replicas: 2,
			Args:     args,
			TLSSecretRef: &corev1.SecretReference{
				Name: "df-tls",
			},
			Authentication: &resourcesv1.Authentication{
				PasswordFromSecret: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "df-password",
					},
					Key: "password",
				},
			},
		},
	}

	Context("Dragonfly TLS creation", func() {
		It("Should create successfully", func() {
			// create the secrets
			cert, key, err := generateSelfSignedCert(name)
			Expect(err).To(BeNil())

			err = k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "df-tls",
					Namespace: namespace,
				},
				Data: map[string][]byte{
					"tls.crt": cert,
					"tls.key": key,
				},
			})
			Expect(err).To(BeNil())

			err = k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "df-password",
					Namespace: namespace,
				},
				StringData: map[string]string{
					"password": "df-pass-1",
				},
			})
			Expect(err).To(BeNil())

			err = k8sClient.Create(ctx, &df)
			Expect(err).To(BeNil())

		})
		It("Resources should exist", func() {
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

			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--tls"))
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--no_tls_on_admin_port"))
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement(fmt.Sprintf("--tls_cert_file=%s/tls.crt", resources.TLSDir)))
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement(fmt.Sprintf("--tls_key_file=%s/tls.key", resources.TLSDir)))
		})

		It("Cleanup", func() {
			var df resourcesv1.Dragonfly
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

var _ = Describe("Dragonfly Dataset Loading Readiness Gate", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "df-loading-test"
	namespace := "default"
	schedule := "*/1 * * * *"

	args := []string{
		"--vmodule=replica=1,server_family=1",
	}

	Context("Dataset loading readiness gate validation", func() {
		BeforeAll(func() {
			// Clean up any existing resource from previous test runs
			var df resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			if apierrors.IsNotFound(err) {
				// Nothing to clean up
				return
			}
			Expect(err).To(BeNil(), "unexpected error getting Dragonfly resource")
			err = k8sClient.Delete(ctx, &df)
			Expect(err).To(BeNil(), "failed to delete existing Dragonfly resource")
			// Polling until the resource is deleted
			Eventually(func() bool {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: namespace,
				}, &df)
				return apierrors.IsNotFound(err)
			}, 1*time.Minute, 2*time.Second).Should(BeTrue(), "existing resource should be deleted")
		})

		It("Should create Dragonfly instance with snapshot", func() {
			err := k8sClient.Create(ctx, &resourcesv1.Dragonfly{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: resourcesv1.DragonflySpec{
					Replicas: 2,
					Args:     args,
					Snapshot: &resourcesv1.Snapshot{
						Cron: schedule,
						PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{
								corev1.ReadWriteOnce,
							},
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse("2Gi"),
								},
							},
						},
					},
				},
			})
			Expect(err).To(BeNil())
		})

		It("Should wait for resources and seed heavy dataset", func() {
			var df resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			Expect(err).To(BeNil())
			GinkgoLogr.Info("Dragonfly CR status", "phase", df.Status.Phase, "isRollingUpdate", df.Status.IsRollingUpdate)

			// Wait for Dragonfly object initialization
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseResourcesCreated, 3*time.Minute)
			Expect(err).To(BeNil(), "Dragonfly should reach PhaseResourcesCreated")

			// With for readiness gate, StatefulSet may take longer as pods wait for dataset loading
			err = waitForStatefulSetReady(ctx, k8sClient, name, namespace, 5*time.Minute)
			Expect(err).To(BeNil(), "StatefulSet should become ready")

			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)
			Expect(err).To(BeNil(), "Dragonfly should reach PhaseReady")

			// Connect and seed heavy dataset (200MB)
			stopChan := make(chan struct{}, 1)
			rc, err := checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6396)
			Expect(err).To(BeNil())
			defer close(stopChan)
			defer rc.Close()

			// Seed dataset using pipeline for efficiency
			// Total data: 200,000 × 1KB = 200MB
			pipe := rc.Pipeline()
			const numKeys = 200000
			const valueSize = 1024 // 1KB per value
			value := strings.Repeat("A", valueSize)

			for i := 0; i < numKeys; i++ {
				pipe.Set(ctx, fmt.Sprintf("key-%d", i), value, 0)
				if (i+1)%1000 == 0 {
					_, err := pipe.Exec(ctx)
					Expect(err).To(BeNil())
					pipe = rc.Pipeline()
				}
			}

			// If numKeys isn't divisible by 1000, executes the leftover commands
			if numKeys%1000 != 0 {
				_, err := pipe.Exec(ctx)
				Expect(err).To(BeNil())
			}

			GinkgoLogr.Info("Dataset seeded", "keys", numKeys)
		})

		It("Should wait for dataset loading during pod restart", func() {
			// Master pod check
			var pods corev1.PodList
			err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				resources.DragonflyNameLabelKey:    name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			var masterPod *corev1.Pod
			for i := range pods.Items {
				if pods.Items[i].Labels[resources.RoleLabelKey] == resources.Master {
					masterPod = &pods.Items[i]
					break
				}
			}
			Expect(masterPod).NotTo(BeNil(), "master pod should exist")

			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 30*time.Second)
			Expect(err).To(BeNil(), "cluster should be Ready before restart")

			// Delete the master pod to trigger restart
			err = k8sClient.Delete(ctx, masterPod)
			Expect(err).To(BeNil())

			// Wait for pod to be recreated
			var restartedPod corev1.Pod
			Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      masterPod.Name,
					Namespace: namespace,
				}, &restartedPod)
				if err != nil {
					return err
				}
				if restartedPod.Status.Phase != corev1.PodRunning {
					return fmt.Errorf("pod not running yet: %s", restartedPod.Status.Phase)
				}
				return nil
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			Expect(restartedPod.Status.Phase).To(Equal(corev1.PodRunning))

			// Check persistence info to verify loading state
			stopChan := make(chan struct{}, 1)
			defer close(stopChan)

			// Verify that loading is in progress initially, or has just completed
			// The readiness gate should prevent the controller from marking the cluster as Ready
			// while loading is happening
			var loadingValue string
			var loadStateValue string
			Eventually(func() error {
				loading, loadState, err := checkPersistenceInfo(ctx, clientset, cfg, &restartedPod, stopChan)
				if err != nil {
					return err
				}

				loadingValue = loading
				loadStateValue = loadState
				return nil
			}, 30*time.Second, 2*time.Second).Should(Succeed())

			// If loading is still in progress, verify cluster is not ready yet
			// This validates the readiness gate is working
			if loadingValue != "0" {
				GinkgoLogr.Info("Dataset still loading", "loading", loadingValue, "load_state", loadStateValue)
				// Cluster should not be Ready while loading
				var df resourcesv1.Dragonfly
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      name,
					Namespace: namespace,
				}, &df)
				Expect(err).To(BeNil())
				Expect(df.Status.Phase).NotTo(Equal(controller.PhaseReady), "cluster should not be Ready while dataset is loading")
			}

			// Poll until loading completes, validate the readiness gate
			Eventually(func() (string, error) {
				loading, _, err := checkPersistenceInfo(ctx, clientset, cfg, &restartedPod, stopChan)
				if err != nil {
					return "", err
				}
				return loading, nil
			}, 3*time.Minute, 2*time.Second).Should(Equal("0"), "dataset should finish loading")

			// Verify load_state is equal to done
			Eventually(func() (string, error) {
				_, loadState, err := checkPersistenceInfo(ctx, clientset, cfg, &restartedPod, stopChan)
				if err != nil {
					return "", err
				}
				return loadState, nil
			}, 30*time.Second, 2*time.Second).Should(Or(Equal("done"), Equal("")), "load_state should be done or empty")

			// After loading completes, verify the pod gets a role label
			// This confirms the controller proceeded after loading finished
			Eventually(func() bool {
				var pod corev1.Pod
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      masterPod.Name,
					Namespace: namespace,
				}, &pod)
				if err != nil {
					return false
				}
				_, hasRole := pod.Labels[resources.RoleLabelKey]
				return hasRole
			}, 1*time.Minute, 2*time.Second).Should(BeTrue(), "pod should have role label after loading completes")

			// Verify cluster becomes Ready only after loading completes
			err = waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)
			Expect(err).To(BeNil(), "cluster should be Ready only after dataset loading completes")
		})

		AfterAll(func() {
			// Clean up resources created during the test
			var df resourcesv1.Dragonfly
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)
			if apierrors.IsNotFound(err) {
				// Nothing to clean up
				return
			}
			Expect(err).To(BeNil(), "unexpected error getting Dragonfly resource during cleanup")
			err = k8sClient.Delete(ctx, &df)
			Expect(err).To(BeNil(), "failed to delete Dragonfly resource during cleanup")
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

func generateSelfSignedCert(commonName string) ([]byte, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %v", err)
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	return certPEM, keyPEM, nil
}

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

	GinkgoLogr.Info("dragonfly phase", "phase", df.Status.Phase, "update", df.Status.IsRollingUpdate)

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
