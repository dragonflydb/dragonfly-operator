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
)

func TestReconcileClientDisconnectWaitsForTheServiceToConverge(t *testing.T) {
	k8s := newStaleEndpointClient(buildDemotionFixture(t))
	srv := startFakeDragonfly(t, k8s.podInEndpoints)

	dfi := newDemotionInstance(k8s, srv.addr())
	defer dfi.Close()

	pod := getDemotionPod(t, k8s)
	require.NoError(t, dfi.replicaOf(t.Context(), pod, newMasterIP))
	require.Contains(t, pod.Annotations, resources.PendingClientDisconnectAnnotationKey,
		"a demoted master must be marked as still owing its clients a disconnect")

	result, err := dfi.reconcileClientDisconnect(t.Context(), pod)
	require.NoError(t, err)
	assert.False(t, srv.sawKill(), "the master service still routes to the pod, so nothing may be killed yet")
	assert.Positive(t, result.RequeueAfter, "a stalled EndpointSlice controller must not leave the demotion unfinished")

	result, err = dfi.reconcileClientDisconnect(t.Context(), pod)
	require.NoError(t, err)
	assert.True(t, srv.sawKill(), "the pod left the master service, so its clients must be disconnected")
	assert.Zero(t, result.RequeueAfter)
	assert.NotContains(t, pod.Annotations, resources.PendingClientDisconnectAnnotationKey)
}

func TestReconcileClientDisconnectFallsThroughOnTimeout(t *testing.T) {
	k8s := newStaleEndpointClient(buildDemotionFixture(t))
	k8s.neverConverges = true
	srv := startFakeDragonfly(t, k8s.podInEndpoints)

	dfi := newDemotionInstance(k8s, srv.addr())
	defer dfi.Close()

	pod := getDemotionPod(t, k8s)
	require.NoError(t, dfi.replicaOf(t.Context(), pod, newMasterIP))

	pod.Annotations[resources.PendingClientDisconnectAnnotationKey] =
		time.Now().Add(-clientDisconnectTimeout - time.Second).UTC().Format(time.RFC3339)

	result, err := dfi.reconcileClientDisconnect(t.Context(), pod)
	require.NoError(t, err)
	assert.True(t, srv.sawKill(), "the wait is bounded, so the disconnect must happen even without convergence")
	assert.Zero(t, result.RequeueAfter)
	assert.NotContains(t, pod.Annotations, resources.PendingClientDisconnectAnnotationKey)
}

func TestReconcileClientDisconnectClearsTheMarkerOnPromotion(t *testing.T) {
	k8s := newStaleEndpointClient(buildDemotionFixture(t))
	srv := startFakeDragonfly(t, k8s.podInEndpoints)

	dfi := newDemotionInstance(k8s, srv.addr())
	defer dfi.Close()

	pod := getDemotionPod(t, k8s)
	require.NoError(t, dfi.replicaOf(t.Context(), pod, newMasterIP))

	// replTakeover promotes a pod without touching the marker.
	pod.Labels[resources.RoleLabelKey] = resources.Master

	result, err := dfi.reconcileClientDisconnect(t.Context(), pod)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)
	assert.False(t, srv.sawKill(), "a pod serving as master must keep its clients")
	assert.NotContains(t, pod.Annotations, resources.PendingClientDisconnectAnnotationKey,
		"a stranded marker keeps the pod in the field index and wakes it on every endpoint event")
}

func TestReconcileClientDisconnectKeepsTheMarkerWhenTheDisconnectFails(t *testing.T) {
	k8s := newStaleEndpointClient(buildDemotionFixture(t))
	srv := startFakeDragonfly(t, k8s.podInEndpoints)
	srv.setFailDisconnect(true)

	dfi := newDemotionInstance(k8s, srv.addr())
	defer dfi.Close()

	pod := getDemotionPod(t, k8s)
	require.NoError(t, dfi.replicaOf(t.Context(), pod, newMasterIP))

	_, err := dfi.reconcileClientDisconnect(t.Context(), pod)
	require.NoError(t, err)

	_, err = dfi.reconcileClientDisconnect(t.Context(), pod)
	require.Equal(t, 1, srv.disconnectAttempts(), "the pod left the service, so a disconnect must have been attempted")
	require.Error(t, err, "a failed disconnect must surface so the reconcile is retried with backoff")
	require.Contains(t, pod.Annotations, resources.PendingClientDisconnectAnnotationKey,
		"clearing the marker after a failed disconnect loses the obligation for good")

	srv.setFailDisconnect(false)

	_, err = dfi.reconcileClientDisconnect(t.Context(), pod)
	require.NoError(t, err)
	assert.True(t, srv.sawKill(), "the retry must actually disconnect once the instance answers again")
	assert.NotContains(t, pod.Annotations, resources.PendingClientDisconnectAnnotationKey)
}
