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
	"fmt"

	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	dflyUserGroup int64 = 999
)

func hasCustomProbes(df *resourcesv1.Dragonfly) bool {
	return (df.Spec.CustomLivenessProbeConfigMap != nil && df.Spec.CustomLivenessProbeConfigMap.Name != "") ||
		(df.Spec.CustomReadinessProbeConfigMap != nil && df.Spec.CustomReadinessProbeConfigMap.Name != "") ||
		(df.Spec.CustomStartupProbeConfigMap != nil && df.Spec.CustomStartupProbeConfigMap.Name != "")
}

// returns the custom ConfigMap name if set, or "<dfName>-<suffix>" otherwise
func probeConfigMapName(dfName, suffix string, custom *corev1.LocalObjectReference) string {
	if custom != nil && custom.Name != "" {
		return custom.Name
	}
	return fmt.Sprintf("%s-%s", dfName, suffix)
}

// append probe ConfigMap volumes and mounts into the StatefulSet
func appendProbeVolumesAndMounts(sts *appsv1.StatefulSet, livenessCM, readinessCM, startupCM string) {
	probes := []struct {
		volumeName    string
		configMapName string
		scriptKey     string
	}{
		{LivenessProbeVolumeName, livenessCM, LivenessScriptKey},
		{ReadinessProbeVolumeName, readinessCM, ReadinessScriptKey},
		{StartupProbeVolumeName, startupCM, StartupScriptKey},
	}
	for _, p := range probes {
		sts.Spec.Template.Spec.Volumes = append(sts.Spec.Template.Spec.Volumes,
			corev1.Volume{
				Name: p.volumeName,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: p.configMapName},
					},
				},
			},
		)
		sts.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			sts.Spec.Template.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{
				Name:      p.volumeName,
				MountPath: ProbeMountPath + "/" + p.scriptKey,
				SubPath:   p.scriptKey,
			},
		)
	}
}

// generateProbeConfigMap builds a ConfigMap owned by the Dragonfly instance for a single probe script.
func generateProbeConfigMap(df *resourcesv1.Dragonfly, name, key, script string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: df.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: df.APIVersion,
					Kind:       df.Kind,
					Name:       df.Name,
					UID:        df.UID,
				},
			},
			Labels:      generateResourceLabels(df),
			Annotations: generateResourceAnnotations(df),
		},
		Data: map[string]string{
			key: script,
		},
	}
}

// GenerateDragonflyResources returns the resources required for a Dragonfly
// Instance
func GenerateDragonflyResources(df *resourcesv1.Dragonfly, defaultDragonflyImage string) ([]client.Object, error) {
	var resources []client.Object

	image := df.Spec.Image
	if image == "" {
		if defaultDragonflyImage != "" {
			image = defaultDragonflyImage
		} else {
			image = fmt.Sprintf("%s:%s", DragonflyImage, Version)
		}
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
			Labels:      generateResourceLabels(df),
			Annotations: generateResourceAnnotations(df),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &df.Spec.Replicas,
			ServiceName: df.Name,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					DragonflyNameLabelKey:     df.Name,
					KubernetesPartOfLabelKey:  KubernetesPartOf,
					KubernetesAppNameLabelKey: KubernetesAppName,
				},
			},
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						DragonflyNameLabelKey:     df.Name,
						KubernetesPartOfLabelKey:  KubernetesPartOf,
						KubernetesAppNameLabelKey: KubernetesAppName,
					},
				},
				Spec: corev1.PodSpec{
					ImagePullSecrets: df.Spec.ImagePullSecrets,
					Containers: []corev1.Container{
						{
							Name:  DragonflyContainerName,
							Image: image,
							Ports: []corev1.ContainerPort{
								{
									Name:          DragonflyPortName,
									ContainerPort: DragonflyPort,
								},
								{
									Name:          DragonflyAdminPortName,
									ContainerPort: DragonflyAdminPort,
								},
							},
							Args: DefaultDragonflyArgs,
							Env: append(df.Spec.Env, corev1.EnvVar{
								// Use the admin port for health checks — it never requires TLS,
								// making probes work correctly on TLS-enabled clusters.
								Name:  "HEALTHCHECK_PORT",
								Value: fmt.Sprintf("%d", DragonflyAdminPort),
							}),
							// Default probes use the image-embedded healthcheck script.
							// When custom probe ConfigMaps are set, these are replaced below.
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

	if df.Spec.EnableReplicationReadinessGate {
		statefulset.Spec.Template.Spec.ReadinessGates = []corev1.PodReadinessGate{
			{ConditionType: corev1.PodConditionType(ReplicationReadyConditionType)},
		}
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
		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("%s=%d", MemcachedPortArg, df.Spec.MemcachedPort))
		statefulset.Spec.Template.Spec.Containers[0].Ports = append(statefulset.Spec.Template.Spec.Containers[0].Ports, corev1.ContainerPort{
			Name:          MemcachedPortName,
			ContainerPort: df.Spec.MemcachedPort,
		})
	}

	if df.Spec.AclFromSecret != nil {
		statefulset.Spec.Template.Spec.Volumes = append(statefulset.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: AclVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: df.Spec.AclFromSecret.Name,
					Items: []corev1.KeyToPath{
						{
							Key:  df.Spec.AclFromSecret.Key,
							Path: AclFileName,
						},
					},
				},
			},
		})

		statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      AclVolumeName,
			MountPath: AclDir,
		})

		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("%s=%s/%s", AclFileArg, AclDir, AclFileName))
	}

	// Doc: https://www.dragonflydb.io/blog/a-preview-of-dragonfly-ssd-tiering
	if df.Spec.Tiering != nil {

		tieringVolumeName := "tiering"
		tieringMountName := "/dragonfly/tiering"
		tieringDirName := "vol"

		if df.Spec.Tiering.PersistentVolumeClaimSpec != nil {
			statefulset.Spec.VolumeClaimTemplates = append(statefulset.Spec.VolumeClaimTemplates, corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:        tieringVolumeName,
					Labels:      generateResourceLabels(df),
					Annotations: generateResourceAnnotations(df),
				},
				Spec: *df.Spec.Tiering.PersistentVolumeClaimSpec,
			})

			statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(
				statefulset.Spec.Template.Spec.Containers[0].VolumeMounts,
				corev1.VolumeMount{
					Name:      tieringVolumeName,
					MountPath: tieringMountName,
				},
			)
		}

		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("--tiered_prefix=%s/%s", tieringMountName, tieringDirName))
	}

	if df.Spec.Snapshot != nil {
		// validate mutual exclusivity of PVC spec and existing PVC name
		if df.Spec.Snapshot.PersistentVolumeClaimSpec != nil && df.Spec.Snapshot.ExistingPersistentVolumeClaimName != "" {
			return nil, fmt.Errorf("persistentVolumeClaimSpec and existingPersistentVolumeClaimName are mutually exclusive")
		}

		// err if pvc is not specified & s3 sir is not present while cron is specified
		if df.Spec.Snapshot.Cron != "" && df.Spec.Snapshot.PersistentVolumeClaimSpec == nil && df.Spec.Snapshot.ExistingPersistentVolumeClaimName == "" && df.Spec.Snapshot.Dir == "" {
			return nil, fmt.Errorf("cron specified without a persistent volume claim")
		}

		snapshotDir := df.Spec.Snapshot.Dir
		if df.Spec.Snapshot.Dir == "" {
			snapshotDir = SnapshotsDir
		}

		if df.Spec.Snapshot.PersistentVolumeClaimSpec != nil {
			// attach and use the PVC if specified
			statefulset.Spec.VolumeClaimTemplates = append(statefulset.Spec.VolumeClaimTemplates, corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: SnapshotsVolumeName,
					Labels: map[string]string{
						DragonflyNameLabelKey:     df.Name,
						KubernetesPartOfLabelKey:  KubernetesPartOf,
						KubernetesAppNameLabelKey: KubernetesAppName,
					},
				},
				Spec: *df.Spec.Snapshot.PersistentVolumeClaimSpec,
			})

			statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      SnapshotsVolumeName,
				MountPath: snapshotDir,
			})
		}

		if df.Spec.Snapshot.ExistingPersistentVolumeClaimName != "" {
			// use an existing PVC
			statefulset.Spec.Template.Spec.Volumes = append(statefulset.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: SnapshotsVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: df.Spec.Snapshot.ExistingPersistentVolumeClaimName,
					},
				},
			})

			statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
				Name:      SnapshotsVolumeName,
				MountPath: snapshotDir,
			})
		}

		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("%s=%s", SnapshotsDirArg, snapshotDir))

		if df.Spec.Snapshot.Cron != "" {
			statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("%s=%s", SnapshotsCronArg, df.Spec.Snapshot.Cron))
		}
	}

	if df.Spec.TLSSecretRef != nil {
		statefulset.Spec.Template.Spec.Volumes = append(statefulset.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: TLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: df.Spec.TLSSecretRef.Name,
				},
			},
		})

		statefulset.Spec.Template.Spec.Containers[0].VolumeMounts = append(statefulset.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      TLSVolumeName,
			ReadOnly:  true,
			MountPath: TLSDir,
		})

		statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, []string{
			// no TLS on admin port by default
			NoTLSOnAdminPortArg,
			TLSArg,
			fmt.Sprintf("%s=%s/%s", TLSCertPathArg, TLSDir, TLSCertFileName),
			fmt.Sprintf("%s=%s/%s", TLSKeyPathArg, TLSDir, TLSKeyFileName),
		}...)
	}

	if df.Spec.Annotations != nil {
		statefulset.Spec.Template.ObjectMeta.Annotations = df.Spec.Annotations
	}

	for key := range df.Spec.Labels {
		// allow df.Spec.Labels to overwrite default labels (e.g. app.kubernetes.io/name)
		statefulset.Spec.Template.ObjectMeta.Labels[key] = df.Spec.Labels[key]
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
								Path: TLSCACertFileName,
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
			statefulset.Spec.Template.Spec.Containers[0].Args = append(statefulset.Spec.Template.Spec.Containers[0].Args, fmt.Sprintf("%s=%s/%s", TLSCACertPathArg, TLSCACertDir, TLSCACertFileName))
		}
	}

	// When custom probe ConfigMaps are set, switch probes to use mounted scripts
	// and wire volumes/mounts. Otherwise keep the default image-embedded healthcheck
	// to avoid triggering a rolling update on operator upgrade.
	if hasCustomProbes(df) {
		c := &statefulset.Spec.Template.Spec.Containers[0]
		c.ReadinessProbe.Exec.Command = []string{"/bin/sh", ProbeMountPath + "/" + ReadinessScriptKey}
		c.LivenessProbe.Exec.Command = []string{"/bin/sh", ProbeMountPath + "/" + LivenessScriptKey}
		c.StartupProbe = &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/sh", ProbeMountPath + "/" + StartupScriptKey},
				},
			},
			FailureThreshold:    60,
			InitialDelaySeconds: 0,
			PeriodSeconds:       5,
			SuccessThreshold:    1,
			TimeoutSeconds:      5,
		}

		livenessConfigMapName := probeConfigMapName(df.Name, LivenessProbeConfigMapSuffix, df.Spec.CustomLivenessProbeConfigMap)
		readinessConfigMapName := probeConfigMapName(df.Name, ReadinessProbeConfigMapSuffix, df.Spec.CustomReadinessProbeConfigMap)
		startupConfigMapName := probeConfigMapName(df.Name, StartupProbeConfigMapSuffix, df.Spec.CustomStartupProbeConfigMap)

		appendProbeVolumesAndMounts(&statefulset,
			livenessConfigMapName, readinessConfigMapName, startupConfigMapName,
		)

		// Generate default ConfigMaps for probes not overridden by the user.
		if df.Spec.CustomLivenessProbeConfigMap == nil || df.Spec.CustomLivenessProbeConfigMap.Name == "" {
			resources = append(resources, generateProbeConfigMap(df, livenessConfigMapName, LivenessScriptKey, defaultLivenessScript))
		}
		if df.Spec.CustomReadinessProbeConfigMap == nil || df.Spec.CustomReadinessProbeConfigMap.Name == "" {
			resources = append(resources, generateProbeConfigMap(df, readinessConfigMapName, ReadinessScriptKey, defaultReadinessScript))
		}
		if df.Spec.CustomStartupProbeConfigMap == nil || df.Spec.CustomStartupProbeConfigMap.Name == "" {
			resources = append(resources, generateProbeConfigMap(df, startupConfigMapName, StartupScriptKey, defaultStartupScript))
		}
	}

	statefulset.Spec.Template.Spec.Containers = mergeNamedSlices(
		statefulset.Spec.Template.Spec.Containers, df.Spec.AdditionalContainers,
		func(c corev1.Container) string { return c.Name })

	statefulset.Spec.Template.Spec.Volumes = mergeNamedSlices(
		statefulset.Spec.Template.Spec.Volumes, df.Spec.AdditionalVolumes,
		func(v corev1.Volume) string { return v.Name })

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
			Labels:      generateResourceLabels(df),
			Annotations: generateResourceAnnotations(df),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				DragonflyNameLabelKey:     df.Name,
				KubernetesAppNameLabelKey: KubernetesAppName,
				RoleLabelKey:              Master,
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
			Name: MemcachedPortName,
			Port: df.Spec.MemcachedPort,
		})
	}

	resources = append(resources, &service)

	pdb := policyv1.PodDisruptionBudget{
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
			Labels:      generateResourceLabels(df),
			Annotations: generateResourceAnnotations(df),
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					DragonflyNameLabelKey:     df.Name,
					KubernetesPartOfLabelKey:  KubernetesPartOf,
					KubernetesAppNameLabelKey: KubernetesAppName,
				},
			},
		},
	}

	// Apply custom PDB configuration
	if df.Spec.Pdb == nil || (df.Spec.Pdb.MaxUnavailable == nil && df.Spec.Pdb.MinAvailable == nil) {
		pdb.Spec.MaxUnavailable = &intstr.IntOrString{
			Type:   intstr.Int,
			IntVal: 1,
		}
	} else if df.Spec.Pdb.MaxUnavailable != nil {
		pdb.Spec.MaxUnavailable = df.Spec.Pdb.MaxUnavailable
	} else if df.Spec.Pdb.MinAvailable != nil {
		pdb.Spec.MinAvailable = df.Spec.Pdb.MinAvailable
	}

	if df.Spec.Replicas > 1 {
		resources = append(resources, &pdb)
	}

	if isNetworkPolicyEnabled(df) {
		np := generateNetworkPolicy(df)
		resources = append(resources, &np)
	}

	return resources, nil
}

func isNetworkPolicyEnabled(df *resourcesv1.Dragonfly) bool {
	return df.Spec.NetworkPolicyEnabled == nil || *df.Spec.NetworkPolicyEnabled
}

func generateNetworkPolicy(df *resourcesv1.Dragonfly) networkingv1.NetworkPolicy {
	protocolTCP := corev1.ProtocolTCP

	instanceSelector := map[string]string{
		DragonflyNameLabelKey:     df.Name,
		KubernetesPartOfLabelKey:  KubernetesPartOf,
		KubernetesAppNameLabelKey: KubernetesAppName,
	}

	clientPortRule := networkingv1.NetworkPolicyIngressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: &protocolTCP,
				Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: DragonflyPort},
			},
		},
	}

	adminPortRule := networkingv1.NetworkPolicyIngressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{
				Protocol: &protocolTCP,
				Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: DragonflyAdminPort},
			},
		},
		From: []networkingv1.NetworkPolicyPeer{
			{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						OperatorControlPlaneLabelKey: OperatorControlPlaneLabelValue,
					},
				},
				NamespaceSelector: &metav1.LabelSelector{},
			},
			{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: instanceSelector,
				},
			},
		},
	}

	ingressRules := []networkingv1.NetworkPolicyIngressRule{clientPortRule, adminPortRule}

	if df.Spec.MemcachedPort != 0 {
		memcachedPortRule := networkingv1.NetworkPolicyIngressRule{
			Ports: []networkingv1.NetworkPolicyPort{
				{
					Protocol: &protocolTCP,
					Port:     &intstr.IntOrString{Type: intstr.Int, IntVal: df.Spec.MemcachedPort},
				},
			},
		}
		ingressRules = append(ingressRules, memcachedPortRule)
	}

	return networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      df.Name,
			Namespace: df.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: df.APIVersion,
					Kind:       df.Kind,
					Name:       df.Name,
					UID:        df.UID,
				},
			},
			Labels:      generateResourceLabels(df),
			Annotations: generateResourceAnnotations(df),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: instanceSelector,
			},
			Ingress:     ingressRules,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
}

// mergeNamedSlices will merge base into override, override takes precendence
func mergeNamedSlices[T any](base, override []T, getName func(T) string) []T {
	existing := make(map[string]bool, len(override))
	for _, item := range override {
		existing[getName(item)] = true
	}

	result := make([]T, len(override))
	copy(result, override)

	for _, item := range base {
		if !existing[getName(item)] {
			result = append(result, item)
		}
	}

	return result
}

func generateResourceLabels(df *resourcesv1.Dragonfly) map[string]string {
	labels := map[string]string{
		KubernetesAppComponentLabelKey: KubernetesAppComponent,
		KubernetesAppInstanceLabelKey:  df.Name,
		KubernetesAppNameLabelKey:      KubernetesAppName,
		KubernetesAppVersionLabelKey:   Version,
		KubernetesPartOfLabelKey:       KubernetesPartOf,
		KubernetesManagedByLabelKey:    DragonflyOperatorName,
		DragonflyNameLabelKey:          df.Name,
	}

	if df.Spec.OwnedObjectsMetadata != nil {
		for key, value := range df.Spec.OwnedObjectsMetadata.Labels {
			labels[key] = value
		}
	}

	return labels
}

func generateResourceAnnotations(df *resourcesv1.Dragonfly) map[string]string {
	annotations := map[string]string{}
	if df.Spec.OwnedObjectsMetadata != nil {
		for key, value := range df.Spec.OwnedObjectsMetadata.Annotations {
			annotations[key] = value
		}
	}

	return annotations
}
