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

package resources

import (
	"context"
	"fmt"

	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	dflyUserGroup int64 = 999
)

const (
	TlsPath             = "/etc/dragonfly-tls"
	TLSCACertDirArg     = "--tls_ca_cert_dir"
	TLSCACertDir        = "/etc/dragonfly/client-ca-cert"
	TLSCACertVolumeName = "client-ca-cert"
)

// GetDragonflyResources returns the resources required for a Dragonfly
// Instance
func GetDragonflyResources(ctx context.Context, df *resourcesv1.Dragonfly) ([]client.Object, error) {
	log := log.FromContext(ctx)
	log.Info(fmt.Sprintf("Creating resources for %s", df.Name))

	var resources []client.Object

	image := df.Spec.Image
	if image == "" {
		image = fmt.Sprintf("%s:%s", DragonflyImage, Version)
	}

	// Create a StatefulSet, Headless Service
	statefulset := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      df.Name,
			Namespace: df.Namespace,
			// Useful for automatically deleting the resources when the Dragonfly object is deleted
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: df.APIVersion,
					Kind:       df.Kind,
					Name:       df.Name,
					UID:        df.UID,
				},
			},
			Labels: map[string]string{
				KubernetesAppComponentLabelKey: "dragonfly",
				KubernetesAppInstanceNameLabel: df.Name,
				KubernetesAppNameLabelKey:      "dragonfly",
				KubernetesAppVersionLabelKey:   Version,
				KubernetesPartOfLabelKey:       "dragonfly",
				KubernetesManagedByLabelKey:    DragonflyOperatorName,
				"app":                          df.Name,
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &df.Spec.Replicas,
			ServiceName: df.Name,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":                     df.Name,
					KubernetesPartOfLabelKey:  "dragonfly",
					KubernetesAppNameLabelKey: "dragonfly",
				},
			},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":                     df.Name,
						KubernetesPartOfLabelKey:  "dragonfly",
						KubernetesAppNameLabelKey: "dragonfly",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "dragonfly",
							Image: image,
							Ports: []corev1.ContainerPort{
								{
									Name:          DragonflyPortName,
									ContainerPort: DragonflyPort,
								},
								{
									Name:          "admin",
									ContainerPort: DragonflyAdminPort,
								},
							},
							Args: DefaultDragonflyArgs,
							Env:  df.Spec.Env,
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"/bin/sh",
											"/usr/local/bin/healthcheck.sh",
										},
									},
								},
								FailureThreshold:    3,
								InitialDelaySeconds: 10,
								PeriodSeconds:       10,
								SuccessThreshold:    1,
								TimeoutSeconds:      5,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{
										Command: []string{
											"/bin/sh",
											"/usr/local/bin/healthcheck.sh",
										},
									},
								},
								FailureThreshold:    3,
								InitialDelaySeconds: 10,
								PeriodSeconds:       10,
								SuccessThreshold:    1,
								TimeoutSeconds:      5,
							},
							ImagePullPolicy: corev1.PullAlways,
						},
					},
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: &dflyUserGroup,
					},
				},
			},
		},
	}

	// set only if resources are specified
	if df.Spec.Resources != nil {
		statefulset.Spec.Template.Spec.Containers[0].Resources = *df.Spec.Resources
	}

	if df.Spec.Args != nil {
		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, df.Spec.Args...)
	}

	if df.Spec.Snapshot != nil {
		// err if pvc is not specified while cron is specified
		if df.Spec.Snapshot.Cron != "" && df.Spec.Snapshot.PersistentVolumeClaimSpec == nil {
			return nil, fmt.Errorf("cron specified without a persistent volume claim")
		}

		if df.Spec.Snapshot.PersistentVolumeClaimSpec != nil {

			// attach and use the PVC if specified
			statefulset.Spec.VolumeClaimTemplates = append(statefulset.Spec.VolumeClaimTemplates, corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "df",
					Labels: map[string]string{
						"app":                     df.Name,
						KubernetesPartOfLabelKey:  "dragonfly",
						KubernetesAppNameLabelKey: "dragonfly",
					},
				},
				Spec: *df.Spec.Snapshot.PersistentVolumeClaimSpec,
			})

			statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      "df",
				MountPath: "/dragonfly/snapshots",
			})
		}

		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, "--dir=/dragonfly/snapshots")
		if df.Spec.Snapshot.Cron != "" {
			statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("--snapshot_cron=%s", df.Spec.Snapshot.Cron))
		}
	}

	if df.Spec.TLSSecretRef != nil {
		statefulset.Spec.Template.Spec.Volumes = append(statefulset.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "dragonfly-tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: df.Spec.TLSSecretRef.Name,
				},
			},
		})

		statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "dragonfly-tls",
			ReadOnly:  true,
			MountPath: TlsPath,
		})

		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, []string{
			// no TLS on admin port by default
			"--no_tls_on_admin_port",
			"--tls",
			fmt.Sprintf("--tls_cert_file=%s/tls.crt", TlsPath),
			fmt.Sprintf("--tls_key_file=%s/tls.key", TlsPath),
		}...)
	}

	if df.Spec.Annotations != nil {
		statefulset.Spec.Template.ObjectMeta.Annotations = df.Spec.Annotations
	}

	if df.Spec.Affinity != nil {
		statefulset.Spec.Template.Spec.Affinity = df.Spec.Affinity
	}

	if df.Spec.Tolerations != nil {
		statefulset.Spec.Template.Spec.Tolerations = df.Spec.Tolerations
	}

	if df.Spec.ServiceAccountName != "" {
		statefulset.Spec.Template.Spec.ServiceAccountName = df.Spec.ServiceAccountName
	}

	if df.Spec.Authentication != nil {
		if df.Spec.Authentication.PasswordFromSecret != nil {
			// load the secret key as a password into env
			statefulset.Spec.Template.Spec.Containers[0].Env = append(statefulset.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{
				// todo: switch to DFLY_requirepass once a new version is released
				Name: "DFLY_PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: df.Spec.Authentication.PasswordFromSecret,
				},
			})
		}

		if df.Spec.Authentication.ClientCaCertSecret != nil {
			// mount the secret as a volume
			statefulset.Spec.Template.Spec.Volumes = append(statefulset.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: TLSCACertVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: df.Spec.Authentication.ClientCaCertSecret.Name,
					},
				},
			})

			// mount it
			statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      TLSCACertVolumeName,
				MountPath: TLSCACertDir,
			})

			// pass it as an arg
			statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("%s=%s", TLSCACertDirArg, TLSCACertDir))

		}
	}

	resources = append(resources, &statefulset)

	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      df.Name,
			Namespace: df.Namespace,
			// Useful for automatically deleting the resources when the Dragonfly object is deleted
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: df.APIVersion,
					Kind:       df.Kind,
					Name:       df.Name,
					UID:        df.UID,
				},
			},
			Labels: map[string]string{
				KubernetesAppComponentLabelKey: "Dragonfly",
				KubernetesAppInstanceNameLabel: df.Name,
				KubernetesAppNameLabelKey:      "dragonfly",
				KubernetesAppVersionLabelKey:   Version,
				KubernetesPartOfLabelKey:       "dragonfly",
				KubernetesManagedByLabelKey:    DragonflyOperatorName,
				"app":                          df.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app":                     df.Name,
				KubernetesAppNameLabelKey: "dragonfly",
				Role:                      Master,
			},
			Ports: []corev1.ServicePort{
				{
					Name: DragonflyPortName,
					Port: DragonflyPort,
				},
			},
		},
	}

	resources = append(resources, &service)

	return resources, nil
}
