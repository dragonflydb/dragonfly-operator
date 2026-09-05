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
	"testing"
	"time"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPodsAwaitingClientDisconnect(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, discoveryv1.AddToScheme(scheme))

	pending := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "df-0",
		Namespace: "default",
		Annotations: map[string]string{
			resources.PendingClientDisconnectAnnotationKey: time.Now().UTC().Format(time.RFC3339),
		},
	}}
	settled := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "df-1", Namespace: "default"}}
	elsewhere := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      "df-0",
		Namespace: "other",
		Annotations: map[string]string{
			resources.PendingClientDisconnectAnnotationKey: time.Now().UTC().Format(time.RFC3339),
		},
	}}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pending, settled, elsewhere).
		WithIndex(&corev1.Pod{}, pendingClientDisconnectIndex, indexPendingClientDisconnect).
		Build()
	r := &DfPodLifeCycleReconciler{Reconciler: Reconciler{Client: k8s, Scheme: scheme}}

	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{
		Name:      "df-xk29p",
		Namespace: "default",
		Labels:    map[string]string{discoveryv1.LabelServiceName: "df"},
	}}

	requests := r.podsAwaitingClientDisconnect(t.Context(), slice)
	require.Len(t, requests, 1, "only the demoted pod of that namespace may be enqueued")
	assert.Equal(t, "df-0", requests[0].Name)
	assert.Equal(t, "default", requests[0].Namespace)

	orphan := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "loose", Namespace: "default"}}
	assert.Empty(t, r.podsAwaitingClientDisconnect(t.Context(), orphan),
		"a slice that backs no service cannot tell us anything about the master service")
}
