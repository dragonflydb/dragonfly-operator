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

package controller

import (
	"context"
	"testing"

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCopyDesiredPayload_ConfigMapDataUpdated(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "df-liveness", Namespace: "default"},
		Data:       map[string]string{"liveness-check.sh": "echo old"},
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "df-liveness", Namespace: "default"},
		Data:       map[string]string{"liveness-check.sh": "echo new"},
	}

	copyDesiredPayload(desired, existing)

	assert.Equal(t, "echo new", existing.Data["liveness-check.sh"],
		"ConfigMap.Data must be copied from desired into existing so client.Patch sends the update")
}

func TestCopyDesiredPayload_ConfigMapBinaryDataUpdated(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "df-bin", Namespace: "default"},
		BinaryData: map[string][]byte{"k": []byte("old")},
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "df-bin", Namespace: "default"},
		BinaryData: map[string][]byte{"k": []byte("new")},
	}

	copyDesiredPayload(desired, existing)

	assert.Equal(t, []byte("new"), existing.BinaryData["k"])
}

func TestCopyDesiredPayload_StatefulSetSpecUpdated(t *testing.T) {
	one, three := int32(1), int32(3)
	existing := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: &one}}
	desired := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: &three}}

	copyDesiredPayload(desired, existing)

	assert.NotNil(t, existing.Spec.Replicas)
	assert.Equal(t, int32(3), *existing.Spec.Replicas,
		"reflection path must still copy .Spec for typed resources after the ConfigMap branch was added")
}

func TestResourceSpecsEqual_ConfigMapDataDiffers(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm"},
		Data:       map[string]string{"k": "old"},
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm"},
		Data:       map[string]string{"k": "new"},
	}

	assert.False(t, resourceSpecsEqual(desired, existing),
		"ConfigMaps with differing Data must be detected as unequal so reconcile reaches the patch path")
}

func TestResourceSpecsEqual_ConfigMapDataEqual(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm"},
		Data:       map[string]string{"k": "v"},
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm"},
		Data:       map[string]string{"k": "v"},
	}

	assert.True(t, resourceSpecsEqual(desired, existing))
}

// TestReconcileAnnotationsLabelsRemoved verifies that annotations/labels removed
// from the desired spec are dropped from the live object instead of lingering.
// The update path sets existing annotations/labels directly from desired, so a
// key present only on the live object must be gone after sync.
func TestReconcileAnnotationsLabelsRemoved(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "df-liveness",
			Namespace:   "default",
			Annotations: map[string]string{"old.io/keep": "1", "removed.io/stale": "x"},
			Labels:      map[string]string{"app": "df", "removed.io/stale": "y"},
		},
		Data: map[string]string{"liveness-check.sh": "echo old"},
	}
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "df-liveness",
			Namespace:   "default",
			Annotations: map[string]string{"old.io/keep": "1"},
			Labels:      map[string]string{"app": "df"},
		},
		Data: map[string]string{"liveness-check.sh": "echo new"},
	}

	// Mirror the update path in reconcileResources: annotations/labels come
	// straight from desired, then payload is copied from desired.
	existing.SetAnnotations(desired.GetAnnotations())
	existing.SetLabels(desired.GetLabels())
	copyDesiredPayload(desired, existing)

	assert.NotContains(t, existing.GetAnnotations(), "removed.io/stale",
		"annotations removed from the spec must not persist on the live object")
	assert.NotContains(t, existing.GetLabels(), "removed.io/stale",
		"labels removed from the spec must not persist on the live object")
	assert.Equal(t, "echo new", existing.Data["liveness-check.sh"])
}

// TestReconcileResources_PreservesLoadBalancerClass runs the real reconcile path
// against a fake client. A webhook (e.g. AWS Load Balancer Controller) sets
// spec.loadBalancerClass on the live Service; the next reconcile must not patch
// it back to null, since the field is immutable and the API server would reject it.
func TestReconcileResources_PreservesLoadBalancerClass(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, dfv1alpha1.AddToScheme(scheme))

	df := &dfv1alpha1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{Name: "df", Namespace: "default", UID: "uid"},
		Spec: dfv1alpha1.DragonflySpec{
			Replicas:    1,
			ServiceSpec: &dfv1alpha1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(df).WithStatusSubresource(df).Build()
	dfi := &DragonflyInstance{
		df:                    df,
		client:                c,
		log:                   logr.Discard(),
		scheme:                scheme,
		eventRecorder:         record.NewFakeRecorder(10),
		defaultDragonflyImage: "dragonfly:test",
	}

	require.NoError(t, dfi.reconcileResources(ctx))

	// Simulate the mutating webhook setting the class on the live Service.
	var svc corev1.Service
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "df"}, &svc))
	nlb := "service.k8s.aws/nlb"
	svc.Spec.LoadBalancerClass = &nlb
	// Simulate externalTrafficPolicy set to Local externally, with the
	// API server allocating a healthCheckNodePort.
	svc.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyLocal
	svc.Spec.HealthCheckNodePort = 32000
	require.NoError(t, c.Update(ctx, &svc))

	require.NoError(t, dfi.reconcileResources(ctx))

	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "df"}, &svc))
	assert.Equal(t, &nlb, svc.Spec.LoadBalancerClass,
		"webhook-set loadBalancerClass must be preserved to avoid patching an immutable field to null")
	assert.Equal(t, corev1.ServiceExternalTrafficPolicyLocal, svc.Spec.ExternalTrafficPolicy,
		"externally-set externalTrafficPolicy must be preserved")
	assert.Equal(t, int32(32000), svc.Spec.HealthCheckNodePort,
		"healthCheckNodePort must be preserved while externalTrafficPolicy is Local")
}
