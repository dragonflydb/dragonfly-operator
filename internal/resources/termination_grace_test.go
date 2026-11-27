package resources

import (
	"testing"

	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
)

func TestGenerateDragonflyResources_TerminationGracePeriodSeconds(t *testing.T) {
	grace := int64(120)
	tests := []struct {
		name     string
		spec     *int64
		expected *int64
	}{
		{name: "unset keeps the Kubernetes default", spec: nil, expected: nil},
		{name: "set is passed to the pod template", spec: &grace, expected: &grace},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			df := &resourcesv1.Dragonfly{
				Spec: resourcesv1.DragonflySpec{TerminationGracePeriodSeconds: tt.spec},
			}

			objs, err := GenerateDragonflyResources(df, "")
			assert.NoError(t, err)

			var sts *appsv1.StatefulSet
			for _, obj := range objs {
				if s, ok := obj.(*appsv1.StatefulSet); ok {
					sts = s
					break
				}
			}

			assert.NotNil(t, sts)
			assert.Equal(t, tt.expected, sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
		})
	}
}
