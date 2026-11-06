package resources

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestMergeNamedSlices_Containers(t *testing.T) {
	base := []corev1.Container{
		{Name: "main", Image: "main:1"},
		{Name: "sidecar", Image: "sidecar:1"},
	}
	override := []corev1.Container{
		{Name: "main", Image: "main:custom"},
		{Name: "metrics", Image: "metrics:1"},
	}

	result := mergeNamedSlices(base, override, func(c corev1.Container) string { return c.Name })

	assert.Len(t, result, 3)
	assert.Equal(t, "main:custom", result[0].Image)
	assert.Equal(t, "metrics:1", result[1].Image)
	assert.Equal(t, "sidecar:1", result[2].Image)
}

func TestMergeNamedSlices_Volumes(t *testing.T) {
	base := []corev1.Volume{
		{Name: "config"},
		{Name: "data"},
	}
	override := []corev1.Volume{
		{Name: "config"}, // override
		{Name: "logs"},
	}

	result := mergeNamedSlices(base, override, func(v corev1.Volume) string { return v.Name })

	assert.Len(t, result, 3)
	assert.Equal(t, "config", result[0].Name)
	assert.Equal(t, "logs", result[1].Name)
	assert.Equal(t, "data", result[2].Name)
}

func TestMergeNamedSlices_EmptyOverride(t *testing.T) {
	base := []corev1.Volume{
		{Name: "default"},
	}
	override := []corev1.Volume{}

	result := mergeNamedSlices(base, override, func(v corev1.Volume) string { return v.Name })

	assert.Len(t, result, 1)
	assert.Equal(t, "default", result[0].Name)
}

func TestMergeNamedSlices_EmptyBase(t *testing.T) {
	base := []corev1.Container{}
	override := []corev1.Container{
		{Name: "user", Image: "user:1"},
	}

	result := mergeNamedSlices(base, override, func(c corev1.Container) string { return c.Name })

	assert.Len(t, result, 1)
	assert.Equal(t, "user", result[0].Name)
}

func newTestDragonfly(replicas int32) *resourcesv1.Dragonfly {
	return &resourcesv1.Dragonfly{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dragonflydb.io/v1alpha1",
			Kind:       "Dragonfly",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-df",
			Namespace: "default",
			UID:       types.UID("test-uid"),
		},
		Spec: resourcesv1.DragonflySpec{
			Replicas: replicas,
		},
	}
}

func TestGenerateDragonflyResources_ReadinessGateDisabled(t *testing.T) {
	df := newTestDragonfly(2)
	df.Spec.EnableReplicationReadinessGate = false

	resources, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	for _, obj := range resources {
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			assert.Empty(t, sts.Spec.Template.Spec.ReadinessGates,
				"readiness gates should be empty when feature is disabled")
			return
		}
	}
	t.Fatal("StatefulSet not found in generated resources")
}

func TestGenerateDragonflyResources_ReadinessGateEnabled(t *testing.T) {
	df := newTestDragonfly(2)
	df.Spec.EnableReplicationReadinessGate = true

	resources, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	for _, obj := range resources {
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			require.Len(t, sts.Spec.Template.Spec.ReadinessGates, 1,
				"should have exactly one readiness gate when feature is enabled")
			assert.Equal(t,
				corev1.PodConditionType(ReplicationReadyConditionType),
				sts.Spec.Template.Spec.ReadinessGates[0].ConditionType,
			)
			return
		}
	}
	t.Fatal("StatefulSet not found in generated resources")
}

func TestGenerateDragonflyResources_PodManagementPolicyDefault(t *testing.T) {
	df := newTestDragonfly(2)

	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)
	assert.Empty(t, sts.Spec.PodManagementPolicy,
		"an unset policy must leave the StatefulSet on the Kubernetes OrderedReady default")
}

func TestGenerateDragonflyResources_PodManagementPolicyParallel(t *testing.T) {
	df := newTestDragonfly(3)
	df.Spec.PodManagementPolicy = string(appsv1.ParallelPodManagement)

	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)
	assert.Equal(t, appsv1.ParallelPodManagement, sts.Spec.PodManagementPolicy)
}

func TestGenerateDragonflyResources_PodMetadataLabels(t *testing.T) {
	df := newTestDragonfly(1)
	df.Spec.PodMetadata = &resourcesv1.MetadataSpec{
		Labels:      map[string]string{"team": "infra"},
		Annotations: map[string]string{"prometheus.io/scrape": "true"},
	}

	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)
	assert.Equal(t, "infra", sts.Spec.Template.Labels["team"])
	assert.Equal(t, "true", sts.Spec.Template.Annotations["prometheus.io/scrape"])
}

func TestGenerateDragonflyResources_PodMetadataWinsOverDeprecated(t *testing.T) {
	df := newTestDragonfly(1)
	df.Spec.Labels = map[string]string{"env": "staging"}
	df.Spec.Annotations = map[string]string{"note": "old"}
	df.Spec.PodMetadata = &resourcesv1.MetadataSpec{
		Labels:      map[string]string{"env": "prod"},
		Annotations: map[string]string{"note": "new"},
	}

	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)
	assert.Equal(t, "prod", sts.Spec.Template.Labels["env"], "podMetadata should win over deprecated labels")
	assert.Equal(t, "new", sts.Spec.Template.Annotations["note"], "podMetadata should win over deprecated annotations")
}

func TestGenerateDragonflyResources_RejectProtectedPodLabel(t *testing.T) {
	for _, field := range []struct {
		name   string
		spec   func(*resourcesv1.DragonflySpec)
		errMsg string
	}{
		{
			name:   "spec.labels",
			spec:   func(s *resourcesv1.DragonflySpec) { s.Labels = map[string]string{KubernetesAppNameLabelKey: "x"} },
			errMsg: "spec.labels",
		},
		{
			name: "spec.podMetadata.labels",
			spec: func(s *resourcesv1.DragonflySpec) {
				s.PodMetadata = &resourcesv1.MetadataSpec{Labels: map[string]string{KubernetesPartOfLabelKey: "x"}}
			},
			errMsg: "spec.podMetadata.labels",
		},
		{
			name: "spec.ownedObjectsMetadata.labels",
			spec: func(s *resourcesv1.DragonflySpec) {
				s.OwnedObjectsMetadata = &resourcesv1.MetadataSpec{Labels: map[string]string{DragonflyNameLabelKey: "x"}}
			},
			errMsg: "spec.ownedObjectsMetadata.labels",
		},
		{
			name: "spec.serviceSpec.labels",
			spec: func(s *resourcesv1.DragonflySpec) {
				s.ServiceSpec = &resourcesv1.ServiceSpec{Labels: map[string]string{KubernetesManagedByLabelKey: "x"}}
			},
			errMsg: "spec.serviceSpec.labels",
		},
	} {
		t.Run(field.name, func(t *testing.T) {
			df := newTestDragonfly(1)
			field.spec(&df.Spec)
			_, err := GenerateDragonflyResources(df, "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), field.errMsg)
		})
	}
}

func TestGenerateDragonflyResources_OwnedObjectsMetadataLabels(t *testing.T) {
	df := newTestDragonfly(2)
	df.Spec.OwnedObjectsMetadata = &resourcesv1.MetadataSpec{
		Labels:      map[string]string{"team": "infra"},
		Annotations: map[string]string{"owner": "platform"},
	}

	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)
	assert.Equal(t, "infra", sts.Labels["team"])
	assert.Equal(t, "platform", sts.Annotations["owner"])
}

func findStatefulSet(objs []client.Object) *appsv1.StatefulSet {
	for _, obj := range objs {
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			return sts
		}
	}
	return nil
}

func findConfigMap(objs []client.Object, name string) *corev1.ConfigMap {
	for _, obj := range objs {
		if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == name {
			return cm
		}
	}
	return nil
}

func TestProbeConfigMaps_NotGeneratedByDefault(t *testing.T) {
	df := newTestDragonfly(1)
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	assert.Nil(t, findConfigMap(objs, "test-df-liveness-probe"), "no ConfigMaps without custom probes")
	assert.Nil(t, findConfigMap(objs, "test-df-readiness-probe"))
	assert.Nil(t, findConfigMap(objs, "test-df-startup-probe"))
}

func TestProbeConfigMaps_GeneratedWhenCustomProbeSet(t *testing.T) {
	df := newTestDragonfly(1)
	df.Spec.CustomLivenessProbeConfigMap = &corev1.LocalObjectReference{Name: "my-liveness"}
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	// custom liveness overrides the default, so no default ConfigMap generated
	assert.Nil(t, findConfigMap(objs, "test-df-liveness-probe"))

	// defaults generated for probes not overridden
	readiness := findConfigMap(objs, "test-df-readiness-probe")
	require.NotNil(t, readiness)
	assert.Contains(t, readiness.Data, "readiness-check.sh")

	startup := findConfigMap(objs, "test-df-startup-probe")
	require.NotNil(t, startup)
	assert.Contains(t, startup.Data, "startup-check.sh")
}

func TestProbeConfigMaps_AllCustomNoneGenerated(t *testing.T) {
	df := newTestDragonfly(1)
	df.Spec.CustomLivenessProbeConfigMap = &corev1.LocalObjectReference{Name: "my-liveness"}
	df.Spec.CustomReadinessProbeConfigMap = &corev1.LocalObjectReference{Name: "my-readiness"}
	df.Spec.CustomStartupProbeConfigMap = &corev1.LocalObjectReference{Name: "my-startup"}
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	assert.Nil(t, findConfigMap(objs, "test-df-liveness-probe"), "no default when all custom")
	assert.Nil(t, findConfigMap(objs, "test-df-readiness-probe"))
	assert.Nil(t, findConfigMap(objs, "test-df-startup-probe"))
}

func TestProbeConfigMaps_EmptyNameTreatedAsNoCustom(t *testing.T) {
	df := newTestDragonfly(1)
	df.Spec.CustomLivenessProbeConfigMap = &corev1.LocalObjectReference{Name: ""}
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	// empty name is treated as no custom probe, keeps default path
	assert.Nil(t, findConfigMap(objs, "test-df-liveness-probe"), "empty name should not trigger custom probes")

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)
	c := sts.Spec.Template.Spec.Containers[0]
	assert.Equal(t, []string{"/bin/sh", "/usr/local/bin/healthcheck.sh"}, c.LivenessProbe.Exec.Command)
	assert.Nil(t, c.StartupProbe, "no startup probe with empty custom name")
}

func TestHealthcheckPortEnvVar_IsAdminPort(t *testing.T) {
	// HEALTHCHECK_PORT always points to the admin port (never requires TLS).
	df := newTestDragonfly(1)
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)

	var hcPort *corev1.EnvVar
	for i := range sts.Spec.Template.Spec.Containers[0].Env {
		env := &sts.Spec.Template.Spec.Containers[0].Env[i]
		if env.Name == "HEALTHCHECK_PORT" {
			hcPort = env
			break
		}
	}
	require.NotNil(t, hcPort, "HEALTHCHECK_PORT env var must be set")
	assert.Equal(t, fmt.Sprintf("%d", DragonflyAdminPort), hcPort.Value)
}

func TestHealthcheckPortEnvVar_UnchangedByCustomRedisPort(t *testing.T) {
	// HEALTHCHECK_PORT stays at the admin port even when the Redis port is customised.
	df := newTestDragonfly(1)
	df.Spec.Args = []string{"--port=6380"}
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)

	var hcPort *corev1.EnvVar
	for i := range sts.Spec.Template.Spec.Containers[0].Env {
		env := &sts.Spec.Template.Spec.Containers[0].Env[i]
		if env.Name == "HEALTHCHECK_PORT" {
			hcPort = env
			break
		}
	}
	require.NotNil(t, hcPort)
	assert.Equal(t, fmt.Sprintf("%d", DragonflyAdminPort), hcPort.Value)
}

func TestProbeVolumesAndMounts_DefaultHasNoProbeVolumes(t *testing.T) {
	df := newTestDragonfly(1)
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)

	for _, v := range sts.Spec.Template.Spec.Volumes {
		assert.NotEqual(t, LivenessProbeVolumeName, v.Name, "no probe volumes without custom probes")
		assert.NotEqual(t, ReadinessProbeVolumeName, v.Name)
		assert.NotEqual(t, StartupProbeVolumeName, v.Name)
	}
}

func TestProbeVolumesAndMounts_PresentWhenCustomProbeSet(t *testing.T) {
	df := newTestDragonfly(1)
	df.Spec.CustomReadinessProbeConfigMap = &corev1.LocalObjectReference{Name: "my-readiness"}
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)

	volumeNames := make(map[string]string)
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if v.ConfigMap != nil {
			volumeNames[v.Name] = v.ConfigMap.Name
		}
	}
	assert.Equal(t, "test-df-liveness-probe", volumeNames[LivenessProbeVolumeName])
	assert.Equal(t, "my-readiness", volumeNames[ReadinessProbeVolumeName])
	assert.Equal(t, "test-df-startup-probe", volumeNames[StartupProbeVolumeName])

	mountPaths := make(map[string]corev1.VolumeMount)
	for _, m := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
		mountPaths[m.Name] = m
	}
	assert.Equal(t, ProbeMountPath+"/"+LivenessScriptKey, mountPaths[LivenessProbeVolumeName].MountPath)
	assert.Equal(t, ProbeMountPath+"/"+ReadinessScriptKey, mountPaths[ReadinessProbeVolumeName].MountPath)
	assert.Equal(t, ProbeMountPath+"/"+StartupScriptKey, mountPaths[StartupProbeVolumeName].MountPath)
}

func TestProbes_DefaultUseImageHealthcheck(t *testing.T) {
	df := newTestDragonfly(1)
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)
	c := sts.Spec.Template.Spec.Containers[0]

	require.NotNil(t, c.LivenessProbe)
	assert.Equal(t, []string{"/bin/sh", "/usr/local/bin/healthcheck.sh"}, c.LivenessProbe.Exec.Command)

	require.NotNil(t, c.ReadinessProbe)
	assert.Equal(t, []string{"/bin/sh", "/usr/local/bin/healthcheck.sh"}, c.ReadinessProbe.Exec.Command)

	assert.Nil(t, c.StartupProbe, "no startup probe without custom probes")
}

func TestProbes_PointToMountedScriptsWhenCustomSet(t *testing.T) {
	df := newTestDragonfly(1)
	df.Spec.CustomStartupProbeConfigMap = &corev1.LocalObjectReference{Name: "my-startup"}
	objs, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	sts := findStatefulSet(objs)
	require.NotNil(t, sts)
	c := sts.Spec.Template.Spec.Containers[0]

	assert.Equal(t, []string{"/bin/sh", ProbeMountPath + "/" + LivenessScriptKey}, c.LivenessProbe.Exec.Command)
	assert.Equal(t, []string{"/bin/sh", ProbeMountPath + "/" + ReadinessScriptKey}, c.ReadinessProbe.Exec.Command)

	require.NotNil(t, c.StartupProbe)
	assert.Equal(t, []string{"/bin/sh", ProbeMountPath + "/" + StartupScriptKey}, c.StartupProbe.Exec.Command)
	assert.Equal(t, int32(60), c.StartupProbe.FailureThreshold)
}

func TestProbeVolumes_CustomConfigMapOverride(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(df *resourcesv1.Dragonfly)
		volumeName    string
		wantCM        string
		defaultCMName string // should NOT appear in generated resources
	}{
		{
			name: "custom liveness",
			setup: func(df *resourcesv1.Dragonfly) {
				df.Spec.CustomLivenessProbeConfigMap = &corev1.LocalObjectReference{Name: "my-liveness"}
			},
			volumeName:    LivenessProbeVolumeName,
			wantCM:        "my-liveness",
			defaultCMName: "test-df-" + LivenessProbeConfigMapSuffix,
		},
		{
			name: "custom readiness",
			setup: func(df *resourcesv1.Dragonfly) {
				df.Spec.CustomReadinessProbeConfigMap = &corev1.LocalObjectReference{Name: "my-readiness"}
			},
			volumeName:    ReadinessProbeVolumeName,
			wantCM:        "my-readiness",
			defaultCMName: "test-df-" + ReadinessProbeConfigMapSuffix,
		},
		{
			name: "custom startup",
			setup: func(df *resourcesv1.Dragonfly) {
				df.Spec.CustomStartupProbeConfigMap = &corev1.LocalObjectReference{Name: "my-startup"}
			},
			volumeName:    StartupProbeVolumeName,
			wantCM:        "my-startup",
			defaultCMName: "test-df-" + StartupProbeConfigMapSuffix,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			df := newTestDragonfly(1)
			tc.setup(df)

			objs, err := GenerateDragonflyResources(df, "")
			require.NoError(t, err)

			// volume should reference the custom ConfigMap
			sts := findStatefulSet(objs)
			require.NotNil(t, sts)

			var found bool
			for _, v := range sts.Spec.Template.Spec.Volumes {
				if v.Name == tc.volumeName {
					require.NotNil(t, v.ConfigMap)
					assert.Equal(t, tc.wantCM, v.ConfigMap.Name)
					found = true
					break
				}
			}
			assert.True(t, found, "volume %q not found", tc.volumeName)

			// default ConfigMap should NOT be generated
			assert.Nil(t, findConfigMap(objs, tc.defaultCMName),
				"default ConfigMap %q should not be generated when custom is set", tc.defaultCMName)
		})
	}
}

func TestGenerateDragonflyResources_ServiceLinksDisabled(t *testing.T) {
	df := newTestDragonfly(1)

	resources, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	for _, obj := range resources {
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			enabled := sts.Spec.Template.Spec.EnableServiceLinks
			require.NotNil(t, enabled,
				"EnableServiceLinks must be set explicitly; nil defaults to true and lets the "+
					"kubelet inject DFLY_-prefixed service variables that the server rejects as flags")
			assert.False(t, *enabled, "service links should be disabled")
			return
		}
	}
	t.Fatal("StatefulSet not found in generated resources")
}

func TestGenerateDragonflyResources_PreStopTakeover(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		df := newTestDragonfly(2)
		df.Spec.EnablePreStopTakeover = enabled

		resources, err := GenerateDragonflyResources(df, "")
		require.NoError(t, err)

		found := false
		for _, obj := range resources {
			sts, ok := obj.(*appsv1.StatefulSet)
			if !ok {
				continue
			}
			found = true
			lc := sts.Spec.Template.Spec.Containers[0].Lifecycle
			if !enabled {
				assert.Nil(t, lc, "no lifecycle expected when feature is disabled")
				continue
			}
			require.NotNil(t, lc)
			require.NotNil(t, lc.PreStop)
			require.NotNil(t, lc.PreStop.Exec)
			assert.Contains(t, lc.PreStop.Exec.Command[2], "REPLTAKEOVER")
		}
		require.True(t, found, "StatefulSet not found in generated resources")
	}
}

func TestGenerateDragonflyResources_PreStopTakeoverTimeouts(t *testing.T) {
	df := newTestDragonfly(2)
	df.Spec.EnablePreStopTakeover = true

	resources, err := GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	for _, obj := range resources {
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			cmd := sts.Spec.Template.Spec.Containers[0].Lifecycle.PreStop.Exec.Command
			require.Len(t, cmd, 6)
			assert.Equal(t, "5", cmd[4], "REPLTAKEOVER timeout in seconds")
			assert.Equal(t, "10", cmd[5], "per-call wall-clock limit in seconds")
			return
		}
	}
	t.Fatal("StatefulSet not found in generated resources")
}

// runPreStopScript runs the preStop script with stub redis-cli and timeout
// binaries and returns the arguments of every redis-cli call.
func runPreStopScript(t *testing.T, info string) []string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	infoFile := filepath.Join(dir, "info.txt")
	require.NoError(t, os.WriteFile(infoFile, []byte(info), 0o600))

	stubs := map[string]string{
		"timeout":   "#!/bin/sh\nshift\nexec \"$@\"\n",
		"redis-cli": "#!/bin/sh\necho \"$@\" >> " + log + "\ncase \"$*\" in *\"info replication\"*) cat " + infoFile + " ;; esac\n",
	}
	for name, body := range stubs {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700))
	}

	cmd := exec.Command("/bin/sh", "-c", defaultPreStopTakeoverScript, "prestop", "5", "10")
	cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
	require.NoError(t, cmd.Run(), "hook must always exit 0")

	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestPreStopScript(t *testing.T) {
	t.Run("takes over on the first online replica", func(t *testing.T) {
		calls := runPreStopScript(t, "role:master\r\nslave0:ip=10.0.0.5,port=6379,state=full_sync,lag=0\r\nslave1:ip=10.0.0.6,port=6379,state=online,lag=0\r\n")
		require.Len(t, calls, 2)
		assert.Equal(t, "-h 10.0.0.6 -p 9999 REPLTAKEOVER 5", calls[1])
	})
	t.Run("does nothing on a replica", func(t *testing.T) {
		calls := runPreStopScript(t, "role:replica\r\n")
		assert.Len(t, calls, 1)
	})
	t.Run("does nothing without an online replica", func(t *testing.T) {
		calls := runPreStopScript(t, "role:master\r\nslave0:ip=10.0.0.5,port=6379,state=full_sync,lag=0\r\n")
		assert.Len(t, calls, 1)
	})
}
