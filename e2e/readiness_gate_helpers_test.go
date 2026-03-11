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

package e2e

import (
	"context"
	"fmt"
	"time"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	. "github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// getMasterReplica returns pointers to the master and one replica pod that are
func getMasterReplica(ctx context.Context, namespace, name string) (*corev1.Pod, *corev1.Pod, error) {
	var pods corev1.PodList
	if err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
		resources.DragonflyNameLabelKey:    name,
		resources.KubernetesPartOfLabelKey: "dragonfly",
	}); err != nil {
		return nil, nil, err
	}

	var master, replica *corev1.Pod
	for i := range pods.Items {
		p := &pods.Items[i]
		role := p.Labels[resources.RoleLabelKey]

		podReady := false
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				podReady = true
				break
			}
		}
		if !podReady {
			continue
		}

		switch role {
		case resources.Master:
			if master == nil {
				master = p
			}
		case resources.Replica:
			if replica == nil {
				replica = p
			}
		}
	}

	if master == nil || replica == nil {
		return nil, nil, fmt.Errorf("could not find ready master+replica (master=%v replica=%v)", master != nil, replica != nil)
	}
	return master, replica, nil
}

// tryEvictPod issues a policy/v1 Eviction against the given pod and returns the
// raw API error so callers can inspect it.
func tryEvictPod(ctx context.Context, pod *corev1.Pod) error {
	return clientset.CoreV1().Pods(pod.Namespace).EvictV1(ctx, &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
	})
}

// startPodStateLogger launches a background goroutine that polls pod state every
// 2 seconds and writes it to GinkgoWriter.
func startPodStateLogger(ctx context.Context, name, namespace string) func() {
	stopCh := make(chan struct{})
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				var pods corev1.PodList
				if err := k8sClient.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels{
					resources.DragonflyNameLabelKey:    name,
					resources.KubernetesPartOfLabelKey: "dragonfly",
				}); err != nil {
					GinkgoWriter.Printf("[pod-logger] list error: %v\n", err)
					continue
				}
				for i := range pods.Items {
					p := &pods.Items[i]
					role := p.Labels[resources.RoleLabelKey]
					podReady := "unknown"
					replReady := "absent"
					for _, c := range p.Status.Conditions {
						if c.Type == corev1.PodReady {
							podReady = string(c.Status)
						}
						if c.Type == corev1.PodConditionType(resources.ReplicationReadyConditionType) {
							replReady = string(c.Status)
						}
					}
					GinkgoWriter.Printf("[pod-logger] pod=%s role=%s PodReady=%s repl-ready=%s phase=%s\n",
						p.Name, role, podReady, replReady, p.Status.Phase)
				}
			}
		}
	}()
	return func() { close(stopCh) }
}
