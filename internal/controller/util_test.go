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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// makePod is a helper that builds a minimal Pod with the given name.
func makePod(name string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

func TestSelectMasterCandidate(t *testing.T) {
	tests := []struct {
		name         string
		pods         []corev1.Pod
		readyPods    map[string]bool // pod names that are considered ready
		wantName     string          // expected winner; "" means nil result
	}{
		{
			name:      "no pods",
			pods:      nil,
			readyPods: nil,
			wantName:  "",
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
			name:      "prefers lowest ordinal (pod-0 ready)",
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isReady := func(p *corev1.Pod) bool {
				return tc.readyPods[p.Name]
			}

			got := selectMasterCandidate(tc.pods, isReady)

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
