package resources

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

// additionalVolumeMounts must be added to the Dragonfly main container. Combined
// with additionalVolumes this lets a user back the snapshot dir with an emptyDir.
func TestAdditionalVolumeMounts(t *testing.T) {
	df := newTestDragonfly(2)
	df.Spec.AdditionalVolumes = []corev1.Volume{
		{Name: "snapshots", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	df.Spec.AdditionalVolumeMounts = []corev1.VolumeMount{
		{Name: "snapshots", MountPath: "/dragonfly/snapshots"},
	}

	objs, err := GenerateDragonflyResources(df, "", "dragonfly-operator-system")
	require.NoError(t, err)
	sts := findStatefulSet(objs)
	require.NotNil(t, sts)

	var vol *corev1.Volume
	for i := range sts.Spec.Template.Spec.Volumes {
		if sts.Spec.Template.Spec.Volumes[i].Name == "snapshots" {
			vol = &sts.Spec.Template.Spec.Volumes[i]
		}
	}
	require.NotNil(t, vol, "additionalVolumes must add the volume to the pod")
	require.NotNil(t, vol.EmptyDir)

	var mount *corev1.VolumeMount
	for i := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
		if sts.Spec.Template.Spec.Containers[0].VolumeMounts[i].Name == "snapshots" {
			mount = &sts.Spec.Template.Spec.Containers[0].VolumeMounts[i]
		}
	}
	require.NotNil(t, mount, "additionalVolumeMounts must be added to the main container")
	assert.Equal(t, "/dragonfly/snapshots", mount.MountPath)
}

// additionalVolumeMounts must land on the Dragonfly main container only, never on
// sidecars added via additionalContainers.
func TestAdditionalVolumeMountsNotAddedToAdditionalContainers(t *testing.T) {
	df := newTestDragonfly(2)
	df.Spec.AdditionalContainers = []corev1.Container{
		{Name: "sidecar", Image: "busybox"},
	}
	df.Spec.AdditionalVolumeMounts = []corev1.VolumeMount{
		{Name: "snapshots", MountPath: "/dragonfly/snapshots"},
	}

	objs, err := GenerateDragonflyResources(df, "", "dragonfly-operator-system")
	require.NoError(t, err)
	sts := findStatefulSet(objs)
	require.NotNil(t, sts)

	hasMount := func(c corev1.Container) bool {
		for _, m := range c.VolumeMounts {
			if m.Name == "snapshots" {
				return true
			}
		}
		return false
	}

	var main, sidecar *corev1.Container
	for i := range sts.Spec.Template.Spec.Containers {
		switch sts.Spec.Template.Spec.Containers[i].Name {
		case DragonflyContainerName:
			main = &sts.Spec.Template.Spec.Containers[i]
		case "sidecar":
			sidecar = &sts.Spec.Template.Spec.Containers[i]
		}
	}
	require.NotNil(t, main, "main dragonfly container must be present")
	require.NotNil(t, sidecar, "sidecar container must be present")

	assert.True(t, hasMount(*main), "additionalVolumeMounts must be on the main container")
	assert.False(t, hasMount(*sidecar), "additionalVolumeMounts must not be on additional containers")
}

// on name collision, the user-supplied mount replaces the operator's default mount.
func TestAdditionalVolumeMountsReplaceOnCollision(t *testing.T) {
	df := newTestDragonfly(2)
	df.Spec.AdditionalVolumeMounts = []corev1.VolumeMount{
		{Name: "custom", MountPath: "/custom"},
	}

	objs, err := GenerateDragonflyResources(df, "", "dragonfly-operator-system")
	require.NoError(t, err)
	sts := findStatefulSet(objs)
	require.NotNil(t, sts)

	count := 0
	for _, m := range sts.Spec.Template.Spec.Containers[0].VolumeMounts {
		if m.Name == "custom" {
			count++
		}
	}
	assert.Equal(t, 1, count, "expected exactly one mount named custom")
}
