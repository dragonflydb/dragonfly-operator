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
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/go-logr/logr"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
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

func TestPodManagementPolicyOrDefault(t *testing.T) {
	assert.Equal(t, appsv1.OrderedReadyPodManagement, podManagementPolicyOrDefault(""))
	assert.Equal(t, appsv1.OrderedReadyPodManagement, podManagementPolicyOrDefault(appsv1.OrderedReadyPodManagement))
	assert.Equal(t, appsv1.ParallelPodManagement, podManagementPolicyOrDefault(appsv1.ParallelPodManagement))
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

// TestReconcileResources_PreservesPodManagementPolicy runs the real reconcile path.
// podManagementPolicy is immutable, so a later spec change must leave the live
// StatefulSet policy in place and record a warning. An omitted spec matches the
// Kubernetes default of OrderedReady and must not warn.
func TestReconcileResources_PreservesPodManagementPolicy(t *testing.T) {
	ctx := context.Background()

	t.Run("mismatch leaves live policy and warns", func(t *testing.T) {
		df := &dfv1alpha1.Dragonfly{
			ObjectMeta: metav1.ObjectMeta{Name: "df", Namespace: "default", UID: "uid"},
			Spec: dfv1alpha1.DragonflySpec{
				Replicas:            1,
				PodManagementPolicy: string(appsv1.ParallelPodManagement),
			},
		}
		c, recorder, dfi := newReconcileFixture(t, df)
		require.NoError(t, dfi.reconcileResources(ctx))
		drainEvents(recorder)

		df.Spec.PodManagementPolicy = string(appsv1.OrderedReadyPodManagement)
		require.NoError(t, dfi.reconcileResources(ctx))

		var sts appsv1.StatefulSet
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "df"}, &sts))
		assert.Equal(t, appsv1.ParallelPodManagement, sts.Spec.PodManagementPolicy,
			"a spec change must not rewrite the immutable live policy")
		assert.Contains(t, drainEvents(recorder),
			"Warning ImmutableField PodManagementPolicy change ignored: delete and recreate the Dragonfly instance to apply it")
	})

	t.Run("omitted policy matches live OrderedReady without warning", func(t *testing.T) {
		df := &dfv1alpha1.Dragonfly{
			ObjectMeta: metav1.ObjectMeta{Name: "df", Namespace: "default", UID: "uid"},
			Spec:       dfv1alpha1.DragonflySpec{Replicas: 1},
		}
		c, recorder, dfi := newReconcileFixture(t, df)
		require.NoError(t, dfi.reconcileResources(ctx))

		// The apiserver defaults an empty policy to OrderedReady on the live object.
		var sts appsv1.StatefulSet
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "df"}, &sts))
		sts.Spec.PodManagementPolicy = appsv1.OrderedReadyPodManagement
		require.NoError(t, c.Update(ctx, &sts))
		drainEvents(recorder)

		require.NoError(t, dfi.reconcileResources(ctx))

		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "default", Name: "df"}, &sts))
		assert.Equal(t, appsv1.OrderedReadyPodManagement, sts.Spec.PodManagementPolicy)
		for _, event := range drainEvents(recorder) {
			assert.NotContains(t, event, "PodManagementPolicy")
		}
	})
}

func newReconcileFixture(t *testing.T, df *dfv1alpha1.Dragonfly) (client.Client, *record.FakeRecorder, *DragonflyInstance) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, dfv1alpha1.AddToScheme(scheme))

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(df).WithStatusSubresource(df).Build()
	recorder := record.NewFakeRecorder(10)
	dfi := &DragonflyInstance{
		df:                    df,
		client:                c,
		log:                   logr.Discard(),
		scheme:                scheme,
		eventRecorder:         recorder,
		defaultDragonflyImage: "dragonfly:test",
	}
	return c, recorder, dfi
}

func drainEvents(recorder *record.FakeRecorder) []string {
	var events []string
	for {
		select {
		case event := <-recorder.Events:
			events = append(events, event)
		default:
			return events
		}
	}
}

// fakeTakeoverServer answers RESP commands with +OK and records the arguments
// of REPLTAKEOVER. HELLO is rejected so the client falls back to RESP2.
func fakeTakeoverServer(t *testing.T) (string, <-chan []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	takeovers := make(chan []string, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					args, err := readRESPArray(r)
					if err != nil {
						return
					}
					switch strings.ToUpper(args[0]) {
					case "HELLO":
						_, err = conn.Write([]byte("-ERR unknown command\r\n"))
					case "REPLTAKEOVER":
						takeovers <- args[1:]
						_, err = conn.Write([]byte("+OK\r\n"))
					default:
						_, err = conn.Write([]byte("+OK\r\n"))
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), takeovers
}

func readRESPArray(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "*")))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if _, err := r.ReadString('\n'); err != nil {
			return nil, err
		}
		arg, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		args = append(args, strings.TrimSuffix(arg, "\r\n"))
	}
	return args, nil
}

// TestReplTakeover_TimeoutShorterThanReadTimeout guards against REPLTAKEOVER
// keeping the old master locked down after the operator's client has already
// timed out. The REPLTAKEOVER timeout is in seconds and must be shorter than
// the client read timeout.
func TestReplTakeover_TimeoutShorterThanReadTimeout(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, dfv1alpha1.AddToScheme(scheme))

	df := &dfv1alpha1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{Name: "df", Namespace: "default"},
	}
	newMaster := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "df-1", Namespace: "default", Labels: map[string]string{}},
		Status:     corev1.PodStatus{PodIP: "10.0.0.2"},
	}
	oldMaster := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "df-0", Namespace: "default"},
		Status:     corev1.PodStatus{PodIP: "10.0.0.1"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(df, newMaster, oldMaster).Build()
	dfi := &DragonflyInstance{
		df:            df,
		client:        c,
		log:           logr.Discard(),
		scheme:        scheme,
		eventRecorder: record.NewFakeRecorder(10),
	}
	defer dfi.Close()

	readTimeout := dfi.getRedisClient("10.0.0.3").Options().ReadTimeout

	addr, takeovers := fakeTakeoverServer(t)
	dfi.redisClients[newMaster.Status.PodIP] = redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})

	require.NoError(t, dfi.replTakeover(ctx, newMaster, oldMaster))

	args := <-takeovers
	require.Len(t, args, 1)
	timeoutSeconds, err := strconv.Atoi(args[0])
	require.NoError(t, err)
	assert.Positive(t, timeoutSeconds)
	assert.Less(t, time.Duration(timeoutSeconds)*time.Second, readTimeout,
		"REPLTAKEOVER timeout (seconds) must be shorter than the client read timeout")
}

// fakeInfoServer answers INFO with the given payload. HELLO is rejected so the
// client falls back to RESP2.
func fakeInfoServer(t *testing.T, info string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					args, err := readRESPArray(r)
					if err != nil {
						return
					}
					switch strings.ToUpper(args[0]) {
					case "INFO":
						_, err = conn.Write([]byte("$" + strconv.Itoa(len(info)) + "\r\n" + info + "\r\n"))
					case "HELLO":
						_, err = conn.Write([]byte("-ERR unknown command\r\n"))
					default:
						_, err = conn.Write([]byte("+OK\r\n"))
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// fakeErrorServer rejects every command, like a server that is busy mid-takeover.
func fakeErrorServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					if _, err := readRESPArray(r); err != nil {
						return
					}
					if _, err := conn.Write([]byte("-ERR server busy\r\n")); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// closedAddr returns an address nothing listens on, so dialing it is refused.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

type preStopPod struct {
	name             string
	ip               string
	role             string // role label; empty means unlabeled
	redisRole        string // role reported by INFO replication
	terminating      bool
	terminatingFor   time.Duration // how long ago deletion was requested; default 1s
	disruptionTarget bool          // DisruptionTarget condition without a deletion timestamp
	restarted        bool          // container restarted since it was configured as a replica
	legacy           bool          // configured before the container ID annotation existed
	infoError        bool          // INFO fails with a server error instead of answering
}

// newPreStopInstance builds an instance whose pods answer INFO replication
// with the given role, so the preStop takeover helpers can run without a cluster.
func newPreStopInstance(t *testing.T, enabled bool, pods ...preStopPod) *DragonflyInstance {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, dfv1alpha1.AddToScheme(scheme))

	df := &dfv1alpha1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{Name: "df", Namespace: "default"},
		Spec:       dfv1alpha1.DragonflySpec{EnablePreStopTakeover: enabled},
	}
	objs := []client.Object{df}
	addrs := map[string]string{}
	for _, p := range pods {
		labels := map[string]string{
			resources.DragonflyNameLabelKey:     "df",
			resources.KubernetesPartOfLabelKey:  resources.KubernetesPartOf,
			resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
		}
		if p.role != "" {
			labels[resources.RoleLabelKey] = p.role
		}
		containerID := "containerd://" + p.name
		annotations := map[string]string{}
		switch {
		case p.legacy:
		case p.restarted:
			annotations[resources.ReplicaContainerIDAnnotationKey] = containerID + "-before-restart"
		default:
			annotations[resources.ReplicaContainerIDAnnotationKey] = containerID
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: p.name, Namespace: "default", Labels: labels, Annotations: annotations},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				PodIP: p.ip,
				Conditions: []corev1.PodCondition{
					{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
				},
				ContainerStatuses: []corev1.ContainerStatus{
					{Name: resources.DragonflyContainerName, Ready: true, ContainerID: containerID},
				},
			},
		}
		if p.disruptionTarget {
			pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
				Type:               corev1.DisruptionTarget,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Second)),
			})
		}
		if p.terminating {
			since := p.terminatingFor
			if since == 0 {
				since = time.Second
			}
			// The API server sets the deletion timestamp to the deletion time plus the grace period.
			deletion := metav1.NewTime(time.Now().Add(30*time.Second - since))
			pod.DeletionTimestamp = &deletion
			pod.DeletionGracePeriodSeconds = ptr.To(int64(30))
			pod.Finalizers = []string{"test/hold"}
		}
		objs = append(objs, pod)
		if p.infoError {
			addrs[p.ip] = fakeErrorServer(t)
		} else {
			addrs[p.ip] = fakeInfoServer(t, "# Replication\r\nrole:"+p.redisRole+"\r\n")
		}
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	dfi := &DragonflyInstance{
		df:            df,
		client:        c,
		log:           logr.Discard(),
		scheme:        scheme,
		eventRecorder: record.NewFakeRecorder(10),
		redisClients:  map[string]*redis.Client{},
	}
	for ip, addr := range addrs {
		dfi.redisClients[ip] = redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	}
	t.Cleanup(dfi.Close)
	return dfi
}

// Regression: after the hook hands the master role to one of several replicas,
// the election must not pick a still-lagging replica over the promoted one.
func TestPreStopTakeoverTarget_AdoptsPromotedReplica(t *testing.T) {
	dfi := newPreStopInstance(t, true,
		preStopPod{name: "df-0", ip: "10.0.0.1", role: resources.Master, redisRole: "master", terminating: true},
		preStopPod{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "master"},
		preStopPod{name: "df-2", ip: "10.0.0.3", role: resources.Replica, redisRole: "replica"},
	)

	target, err := dfi.preStopTakeoverTarget(context.Background())
	require.NoError(t, err)
	require.NotNil(t, target)
	assert.Equal(t, "df-1", target.Name)
}

func TestPreStopTakeoverTarget_Nil(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		pods    []preStopPod
	}{
		{
			name:    "feature disabled",
			enabled: false,
			pods: []preStopPod{
				{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "master"},
			},
		},
		{
			name:    "no replica reports master",
			enabled: true,
			pods: []preStopPod{
				{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "replica"},
				{name: "df-2", ip: "10.0.0.3", role: resources.Replica, redisRole: "replica"},
			},
		},
		{
			name:    "unlabeled pod reporting master is not a takeover target",
			enabled: true,
			pods: []preStopPod{
				{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "replica"},
				{name: "df-2", ip: "10.0.0.3", role: "", redisRole: "master"},
			},
		},
		{
			name:    "ambiguous when two replicas report master",
			enabled: true,
			pods: []preStopPod{
				{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "master"},
				{name: "df-2", ip: "10.0.0.3", role: resources.Replica, redisRole: "master"},
			},
		},
		{
			name:    "terminating replica is ignored",
			enabled: true,
			pods: []preStopPod{
				{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "master", terminating: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dfi := newPreStopInstance(t, tt.enabled, tt.pods...)
			target, err := dfi.preStopTakeoverTarget(context.Background())
			require.NoError(t, err)
			assert.Nil(t, target)
		})
	}
}

func TestPreStopTakeoverInFlight(t *testing.T) {
	t.Run("holds while the terminating master still answers as master", func(t *testing.T) {
		dfi := newPreStopInstance(t, true,
			preStopPod{name: "df-0", ip: "10.0.0.1", role: resources.Master, redisRole: "master", terminating: true},
			preStopPod{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "replica"},
		)
		inFlight, err := dfi.preStopTakeoverInFlight(context.Background())
		require.NoError(t, err)
		assert.True(t, inFlight)
	})
	t.Run("an unlabeled pod reporting master does not release the hold", func(t *testing.T) {
		dfi := newPreStopInstance(t, true,
			preStopPod{name: "df-0", ip: "10.0.0.1", role: resources.Master, redisRole: "master", terminating: true},
			preStopPod{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "replica"},
			preStopPod{name: "df-2", ip: "10.0.0.3", role: "", redisRole: "master"},
		)
		inFlight, err := dfi.preStopTakeoverInFlight(context.Background())
		require.NoError(t, err)
		assert.True(t, inFlight)
	})
	t.Run("released once the old master is no longer a master", func(t *testing.T) {
		dfi := newPreStopInstance(t, true,
			preStopPod{name: "df-0", ip: "10.0.0.1", role: resources.Master, redisRole: "replica", terminating: true},
		)
		inFlight, err := dfi.preStopTakeoverInFlight(context.Background())
		require.NoError(t, err)
		assert.False(t, inFlight)
	})
	t.Run("no hold when the feature is disabled", func(t *testing.T) {
		dfi := newPreStopInstance(t, false,
			preStopPod{name: "df-0", ip: "10.0.0.1", role: resources.Master, redisRole: "master", terminating: true},
		)
		inFlight, err := dfi.preStopTakeoverInFlight(context.Background())
		require.NoError(t, err)
		assert.False(t, inFlight)
	})
}

// Regression: a replica container that restarted comes back as an empty master
// but keeps its replica label. It must not be adopted as the takeover target.
func TestPreStopTakeoverTarget_RejectsRestartedReplica(t *testing.T) {
	tests := []struct {
		name string
		pods []preStopPod
	}{
		{
			name: "restarted replica alone",
			pods: []preStopPod{
				{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "master", restarted: true},
			},
		},
		{
			name: "restarted replica next to a synchronized one",
			pods: []preStopPod{
				{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "master", restarted: true},
				{name: "df-2", ip: "10.0.0.3", role: resources.Replica, redisRole: "replica"},
			},
		},
		{
			name: "replica configured before the container ID annotation existed",
			pods: []preStopPod{
				{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "master", legacy: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dfi := newPreStopInstance(t, true, tt.pods...)
			target, err := dfi.preStopTakeoverTarget(context.Background())
			require.NoError(t, err)
			assert.Nil(t, target)
		})
	}
}

func TestPreStopTakeoverInFlight_INFOErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("holds when INFO fails with a server error", func(t *testing.T) {
		dfi := newPreStopInstance(t, true,
			preStopPod{name: "df-0", ip: "10.0.0.1", role: resources.Master, terminating: true, infoError: true},
		)
		inFlight, err := dfi.preStopTakeoverInFlight(ctx)
		require.NoError(t, err)
		assert.True(t, inFlight)
	})
	t.Run("released when nothing listens on the master port any more", func(t *testing.T) {
		dfi := newPreStopInstance(t, true,
			preStopPod{name: "df-0", ip: "10.0.0.1", role: resources.Master, redisRole: "master", terminating: true},
		)
		dfi.redisClients["10.0.0.1"] = redis.NewClient(&redis.Options{Addr: closedAddr(t), MaxRetries: -1})
		inFlight, err := dfi.preStopTakeoverInFlight(ctx)
		require.NoError(t, err)
		assert.False(t, inFlight)
	})
}

func TestPreStopTakeoverInFlight_Window(t *testing.T) {
	ctx := context.Background()

	t.Run("hold expires once the hook window has passed", func(t *testing.T) {
		dfi := newPreStopInstance(t, true,
			preStopPod{
				name: "df-0", ip: "10.0.0.1", role: resources.Master, redisRole: "master",
				terminating: true, terminatingFor: time.Duration(resources.PreStopMaxSeconds+5) * time.Second,
			},
		)
		inFlight, err := dfi.preStopTakeoverInFlight(ctx)
		require.NoError(t, err)
		assert.False(t, inFlight)
	})
	t.Run("holds for a master that is only a disruption target", func(t *testing.T) {
		dfi := newPreStopInstance(t, true,
			preStopPod{name: "df-0", ip: "10.0.0.1", role: resources.Master, redisRole: "master", disruptionTarget: true},
		)
		inFlight, err := dfi.preStopTakeoverInFlight(ctx)
		require.NoError(t, err)
		assert.True(t, inFlight)
	})
}

// replicaOf records the container it configured, which is what lets the
// takeover lookup tell a replica that took over from one that restarted.
func TestReplicaOf_RecordsContainerID(t *testing.T) {
	dfi := newPreStopInstance(t, true,
		preStopPod{name: "df-1", ip: "10.0.0.2", role: resources.Replica, redisRole: "master", legacy: true},
	)
	ctx := context.Background()

	var pod corev1.Pod
	require.NoError(t, dfi.client.Get(ctx, client.ObjectKey{Namespace: "default", Name: "df-1"}, &pod))
	require.NotContains(t, pod.Annotations, resources.ReplicaContainerIDAnnotationKey)

	require.NoError(t, dfi.replicaOf(ctx, &pod, "10.0.0.1"))

	var got corev1.Pod
	require.NoError(t, dfi.client.Get(ctx, client.ObjectKey{Namespace: "default", Name: "df-1"}, &got))
	assert.Equal(t, "containerd://df-1", got.Annotations[resources.ReplicaContainerIDAnnotationKey])
}
