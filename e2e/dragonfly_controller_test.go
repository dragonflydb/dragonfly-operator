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
	"time"

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

	df := resourcesv1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: resourcesv1.DragonflySpec{
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
				ClientCaCertSecret: &corev1.SecretReference{
					Name: "df-client-ca-certs",
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
		It("Should create successfully", func() {
			// create the secret
			err := k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "df-client-ca-certs",
					Namespace: namespace,
				},
				StringData: map[string]string{
					"ca.crt": "foo",
				},
			})
			Expect(err).To(BeNil())

			// create the secret
			err = k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "df-secret",
					Namespace: namespace,
				},
				StringData: map[string]string{
					"password": "df-pass-1",
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
				Name: "DFLY_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: df.Spec.Authentication.PasswordFromSecret,
				},
			}))

			// ClientCACertSecret
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement(fmt.Sprintf("%s=%s", resources.TLSCACertDirArg, resources.TLSCACertDir)))
			Expect(ss.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(corev1.VolumeMount{
				Name:      resources.TLSCACertVolumeName,
				MountPath: resources.TLSCACertDir,
			}))
			Expect(ss.Spec.Template.Spec.Volumes).To(ContainElement(corev1.Volume{
				Name: resources.TLSCACertVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  df.Spec.Authentication.ClientCaCertSecret.Name,
						DefaultMode: func() *int32 { i := int32(420); return &i }(),
					},
				},
			}))
		})

		It("Check for pod values", func() {
			// Check if there are relevant pods with expected roles
			var pods corev1.PodList
			err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())

			// 3 pod replicas = 1 master + 2 replicas
			Expect(pods.Items).To(HaveLen(3))

		})

		It("Check for connectivity", func() {
			stopChan := make(chan struct{}, 1)
			rc, err := InitRunCmd(ctx, stopChan, name, namespace, "df-pass-1")
			defer close(stopChan)
			Expect(err).To(BeNil())
			err = rc.Start(ctx)
			Expect(err).To(BeNil())
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
			expectedArgs := append(resources.DefaultDragonflyArgs, newArgs...)
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElements(expectedArgs))

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

var _ = Describe("Dragonfly PVC Test", Ordered, FlakeAttempts(3), func() {

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
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			})
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

			// check if the pvc is created
			var pvcs corev1.PersistentVolumeClaimList
			err = k8sClient.List(ctx, &pvcs, client.InNamespace(namespace), client.MatchingLabels{
				"app":                              name,
				resources.KubernetesPartOfLabelKey: "dragonfly",
			})
			Expect(err).To(BeNil())
			Expect(pvcs.Items).To(HaveLen(1))
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement(fmt.Sprintf("--snapshot_cron=%s", schedule)))

			// TODO: Do data insert testing
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

var _ = Describe("Dragonfly TLS tests", Ordered, FlakeAttempts(3), func() {
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
			Env: []corev1.EnvVar{
				{
					Name:  "DFLY_PASSWORD",
					Value: "df-pass-1",
				},
			},
			TLSSecretRef: &corev1.SecretReference{
				Name: "df-tls",
			},
		},
	}

	Context("Dragonfly TLS creation", func() {
		It("Should create successfully", func() {
			// create the secret
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

			err = k8sClient.Create(ctx, &df)
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

			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--tls"))
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--no_tls_on_admin_port"))
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement(fmt.Sprintf("--tls_cert_file=%s/tls.crt", resources.TlsPath)))
			Expect(ss.Spec.Template.Spec.Containers[0].Args).To(ContainElement(fmt.Sprintf("--tls_key_file=%s/tls.key", resources.TlsPath)))
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
