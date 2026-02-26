package resources

import (
	"testing"

	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
