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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// makePod is a helper that builds a minimal Pod with the given name and optional labels.
func makePod(name string, labels ...map[string]string) corev1.Pod {
	l := map[string]string{}
	if len(labels) > 0 {
		l = labels[0]
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l},
	}
}

func makeReplicaPod(name string) corev1.Pod {
	return makePod(name, map[string]string{resources.RoleLabelKey: resources.Replica})
}

func TestSelectMasterCandidate(t *testing.T) {
	tests := []struct {
		name      string
		pods      []corev1.Pod
		readyPods map[string]bool
		offsets   map[string]int64 // per-pod offsets
		wantName  string
	}{
		{
			name:     "no pods",
			pods:     nil,
			wantName: "",
		},
		{
			name:      "all pods unready",
			pods:      []corev1.Pod{makePod("df-0"), makePod("df-1"), makePod("df-2")},
			readyPods: map[string]bool{},
			wantName:  "",
		},
		{
			name:      "single ready pod",
			pods:      []corev1.Pod{makePod("df-0")},
			readyPods: map[string]bool{"df-0": true},
			wantName:  "df-0",
		},
		{
			name:      "prefers lowest ordinal when all equal",
			pods:      []corev1.Pod{makePod("df-2"), makePod("df-0"), makePod("df-1")},
			readyPods: map[string]bool{"df-0": true, "df-1": true, "df-2": true},
			wantName:  "df-0",
		},
		{
			name:      "skips unready pods, picks next lowest ordinal",
			pods:      []corev1.Pod{makePod("df-0"), makePod("df-1"), makePod("df-2")},
			readyPods: map[string]bool{"df-1": true, "df-2": true},
			wantName:  "df-1",
		},
		{
			name:      "only highest ordinal is ready",
			pods:      []corev1.Pod{makePod("df-0"), makePod("df-1"), makePod("df-2")},
			readyPods: map[string]bool{"df-2": true},
			wantName:  "df-2",
		},
		{
			name:      "pods with non-numeric suffixes get lowest priority",
			pods:      []corev1.Pod{makePod("df-bad"), makePod("df-1")},
			readyPods: map[string]bool{"df-bad": true, "df-1": true},
			wantName:  "df-1",
		},
		{
			name:      "replica preferred over non-replica even with higher ordinal",
			pods:      []corev1.Pod{makePod("df-0"), makeReplicaPod("df-1")},
			readyPods: map[string]bool{"df-0": true, "df-1": true},
			wantName:  "df-1",
		},
		{
			name:      "highest offset replica wins",
			pods:      []corev1.Pod{makeReplicaPod("df-1"), makeReplicaPod("df-2")},
			readyPods: map[string]bool{"df-1": true, "df-2": true},
			offsets:   map[string]int64{"df-1": 500, "df-2": 1000},
			wantName:  "df-2",
		},
		{
			name:      "equal offset replicas tie-break by ordinal",
			pods:      []corev1.Pod{makeReplicaPod("df-2"), makeReplicaPod("df-1")},
			readyPods: map[string]bool{"df-1": true, "df-2": true},
			offsets:   map[string]int64{"df-1": 500, "df-2": 500},
			wantName:  "df-1",
		},
		{
			name:      "empty restarted master loses to replica with zero offset",
			pods:      []corev1.Pod{makePod("df-0"), makeReplicaPod("df-1"), makeReplicaPod("df-2")},
			readyPods: map[string]bool{"df-0": true, "df-1": true, "df-2": true},
			wantName:  "df-1",
		},
		{
			name:      "all non-replicas falls back to lowest ordinal",
			pods:      []corev1.Pod{makePod("df-0"), makePod("df-1")},
			readyPods: map[string]bool{"df-0": true, "df-1": true},
			wantName:  "df-0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isReady := func(p *corev1.Pod) bool {
				return tc.readyPods[p.Name]
			}
			getCandidate := func(p *corev1.Pod) MasterCandidate {
				c := MasterCandidate{Pod: p, IsReplica: isReplica(p)}
				if tc.offsets != nil {
					c.Offset = tc.offsets[p.Name]
				}
				return c
			}

			got := selectMasterCandidate(tc.pods, isReady, getCandidate)

			if tc.wantName == "" {
				if got != nil {
					t.Errorf("expected nil, got pod %q", got.Name)
				}
				return
			}

			if got == nil {
				t.Errorf("expected pod %q, got nil", tc.wantName)
				return
			}

			if got.Name != tc.wantName {
				t.Errorf("expected pod %q, got %q", tc.wantName, got.Name)
			}
		})
	}
}

func TestClientListenerAddress(t *testing.T) {
	tests := []struct {
		name  string
		podIp string
		want  string
	}{
		{
			name:  "ipv4",
			podIp: "10.42.1.7",
			want:  "10.42.1.7:6379",
		},
		{
			name:  "ipv6",
			podIp: "fd00::1",
			want:  "fd00::1:6379",
		},
		{
			name:  "bracketed ipv6",
			podIp: "[fd00::1]",
			want:  "fd00::1:6379",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clientListenerAddress(tc.podIp); got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestPendingClientDisconnectSince(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantPending bool
		wantExpired bool
	}{
		{
			name:        "no annotation",
			wantPending: false,
		},
		{
			name:        "valid timestamp",
			annotations: map[string]string{resources.PendingClientDisconnectAnnotationKey: time.Now().UTC().Format(time.RFC3339)},
			wantPending: true,
			wantExpired: false,
		},
		{
			name:        "unparsable value",
			annotations: map[string]string{resources.PendingClientDisconnectAnnotationKey: "true"},
			wantPending: true,
			wantExpired: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "df-0", Annotations: tc.annotations}}

			demotedAt, pending := pendingClientDisconnectSince(&pod)
			if pending != tc.wantPending {
				t.Fatalf("expected pending %v, got %v", tc.wantPending, pending)
			}
			if !pending {
				return
			}

			if expired := time.Since(demotedAt) >= clientDisconnectTimeout; expired != tc.wantExpired {
				t.Errorf("expected expired %v, got %v", tc.wantExpired, expired)
			}
		})
	}
}
