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
	AclPath             = "/var/lib/dragonfly"
	TLSCACertDirArg     = "--tls_ca_cert_file"
	TLSCACertDir        = "/etc/dragonfly/tls"
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
				DragonflyNameLabelKey:          df.Name,
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &df.Spec.Replicas,
			ServiceName: df.Name,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					DragonflyNameLabelKey:     df.Name,
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
						DragonflyNameLabelKey:     df.Name,
						KubernetesPartOfLabelKey:  "dragonfly",
						KubernetesAppNameLabelKey: "dragonfly",
					},
				},
				Spec: corev1.PodSpec{
					ImagePullSecrets: df.Spec.ImagePullSecrets,
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
							Env: append(df.Spec.Env, corev1.EnvVar{
								Name:  "HEALTHCHECK_PORT",
								Value: fmt.Sprintf("%d", DragonflyAdminPort),
							}),
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
							ImagePullPolicy: df.Spec.ImagePullPolicy,
						},
					},
				},
			},
		},
	}

	if len(df.Spec.InitContainers) > 0 {
		statefulset.Spec.Template.Spec.InitContainers = df.Spec.InitContainers
	}

	// Skip Assigning FileSystem Group. Required for platforms such as Openshift that require IDs to not be set, as it injects a fixed randomized ID per namespace into all pods.
	// Skip Assigning FileSystem Group if podSecurityContext is set as well.
	if !df.Spec.SkipFSGroup && df.Spec.PodSecurityContext == nil {
		statefulset.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
			FSGroup: &dflyUserGroup,
		}
	}

	// set podSecurityContext if one is specified
	if df.Spec.PodSecurityContext != nil {
		statefulset.Spec.Template.Spec.SecurityContext = df.Spec.PodSecurityContext
	}

	// set containerSecurityContext if one is specified
	if df.Spec.ContainerSecurityContext != nil {
		statefulset.Spec.Template.Spec.Containers[0].SecurityContext = df.Spec.ContainerSecurityContext
	}

	// set only if resources are specified
	if df.Spec.Resources != nil {
		statefulset.Spec.Template.Spec.Containers[0].Resources = *df.Spec.Resources
	}

	if df.Spec.Args != nil {
		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, df.Spec.Args...)
	}
	if df.Spec.MemcachedPort != 0 {
		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("--memcached_port=%d", df.Spec.MemcachedPort))
		statefulset.Spec.Template.Spec.Containers[0].Ports = append(statefulset.Spec.Template.Spec.Containers[0].Ports, corev1.ContainerPort{
			Name:          "memcached",
			ContainerPort: df.Spec.MemcachedPort,
		})
	}

	if df.Spec.AclFromSecret != nil {
		statefulset.Spec.Template.Spec.Volumes = append(statefulset.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "dragonfly-acl",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: df.Spec.AclFromSecret.Name,
					Items: []corev1.KeyToPath{
						{
							Key:  df.Spec.AclFromSecret.Key,
							Path: "dragonfly.acl",
						},
					},
				},
			},
		})

		statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "dragonfly-acl",
			MountPath: AclPath,
		})

		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("--aclfile=%s/dragonfly.acl", AclPath))
	}

	if df.Spec.Snapshot != nil {
		// err if pvc is not specified & s3 sir is not present while cron is specified
		if df.Spec.Snapshot.Cron != "" && df.Spec.Snapshot.PersistentVolumeClaimSpec == nil && df.Spec.Snapshot.Dir == "" {
			return nil, fmt.Errorf("cron specified without a persistent volume claim")
		}

		dir := "/dragonfly/snapshots"
		if df.Spec.Snapshot.Dir != "" {
			dir = df.Spec.Snapshot.Dir
		}

		if df.Spec.Snapshot.PersistentVolumeClaimSpec != nil {
			// attach and use the PVC if specified
			statefulset.Spec.VolumeClaimTemplates = append(statefulset.Spec.VolumeClaimTemplates, corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "df",
					Labels: map[string]string{
						DragonflyNameLabelKey:     df.Name,
						KubernetesPartOfLabelKey:  "dragonfly",
						KubernetesAppNameLabelKey: "dragonfly",
					},
				},
				Spec: *df.Spec.Snapshot.PersistentVolumeClaimSpec,
			})

			statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      "df",
				MountPath: dir,
			})
		}

		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("--dir=%s", dir))

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

	for key := range df.Spec.Labels {
		// Make sure we do not overwrite any existing labels
		if _, ok := statefulset.Spec.Template.ObjectMeta.Labels[key]; !ok {
			statefulset.Spec.Template.ObjectMeta.Labels[key] = df.Spec.Labels[key]
		}
	}

	if df.Spec.Affinity != nil {
		statefulset.Spec.Template.Spec.Affinity = df.Spec.Affinity
	}

	if df.Spec.NodeSelector != nil {
		statefulset.Spec.Template.Spec.NodeSelector = df.Spec.NodeSelector
	}

	if df.Spec.Tolerations != nil {
		statefulset.Spec.Template.Spec.Tolerations = df.Spec.Tolerations
	}

	if df.Spec.TopologySpreadConstraints != nil {
		statefulset.Spec.Template.Spec.TopologySpreadConstraints = df.Spec.TopologySpreadConstraints
	}

	if df.Spec.ServiceAccountName != "" {
		statefulset.Spec.Template.Spec.ServiceAccountName = df.Spec.ServiceAccountName
	}

	if df.Spec.PriorityClassName != "" {
		statefulset.Spec.Template.Spec.PriorityClassName = df.Spec.PriorityClassName
	}

	if df.Spec.Authentication != nil {
		if df.Spec.Authentication.PasswordFromSecret != nil {
			// load the secret key as a password into env
			statefulset.Spec.Template.Spec.Containers[0].Env = append(statefulset.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{
				Name: "DFLY_requirepass",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: df.Spec.Authentication.PasswordFromSecret,
				},
			})
		}

		if df.Spec.Authentication.ClientCaCertSecret != nil {
			// mount the secrets as a volume
			statefulset.Spec.Template.Spec.Volumes = append(statefulset.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: TLSCACertVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: df.Spec.Authentication.ClientCaCertSecret.Name,
						Items: []corev1.KeyToPath{
							{
								Key:  df.Spec.Authentication.ClientCaCertSecret.Key,
								Path: "ca.crt",
							},
						},
					},
				},
			})

			// mount it
			statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      TLSCACertVolumeName,
				MountPath: TLSCACertDir,
			})

			// pass it as an arg
			statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("%s=%s/ca.crt", TLSCACertDirArg, TLSCACertDir))
		}
	}

	resources = append(resources, &statefulset)

	serviceName := df.Name
	if df.Spec.ServiceSpec != nil && df.Spec.ServiceSpec.Name != "" {
		serviceName = df.Spec.ServiceSpec.Name
	}

	service := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
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
				DragonflyNameLabelKey:          df.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				DragonflyNameLabelKey:     df.Name,
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

	if df.Spec.ServiceSpec != nil {
		service.Spec.Type = df.Spec.ServiceSpec.Type
		service.Annotations = df.Spec.ServiceSpec.Annotations
		service.Labels = df.Spec.ServiceSpec.Labels
		service.Spec.Ports[0].NodePort = df.Spec.ServiceSpec.NodePort
	}
	if df.Spec.MemcachedPort != 0 {
		service.Spec.Ports = append(service.Spec.Ports, corev1.ServicePort{
			Name: "memcached",
			Port: df.Spec.MemcachedPort,
		})
	}

	resources = append(resources, &service)

	return resources, nil
}
