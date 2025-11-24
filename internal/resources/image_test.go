package resources

import (
	"fmt"
	"testing"

	resourcesv1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
)

func TestGenerateDragonflyResources_ImageResolution(t *testing.T) {
	tests := []struct {
		name          string
		crdImage      string
		defaultImage  string
		expectedImage string
	}{
		{
			name:          "CRD image takes precedence",
			crdImage:      "crd-image:v1",
			defaultImage:  "default-image:v1",
			expectedImage: "crd-image:v1",
		},
		{
			name:          "Default image used when CRD image is empty",
			crdImage:      "",
			defaultImage:  "default-image:v1",
			expectedImage: "default-image:v1",
		},
		{
			name:          "Hardcoded default used when both are empty",
			crdImage:      "",
			defaultImage:  "",
			expectedImage: fmt.Sprintf("%s:%s", DragonflyImage, Version),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			df := &resourcesv1.Dragonfly{
				Spec: resourcesv1.DragonflySpec{
					Image: tt.crdImage,
				},
			}

			objs, err := GenerateDragonflyResources(df, tt.defaultImage)
			assert.NoError(t, err)

			var sts *appsv1.StatefulSet
			for _, obj := range objs {
				if s, ok := obj.(*appsv1.StatefulSet); ok {
					sts = s
					break
				}
			}

			assert.NotNil(t, sts)
			assert.Equal(t, tt.expectedImage, sts.Spec.Template.Spec.Containers[0].Image)
		})
	}
}
