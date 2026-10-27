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

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	dragonflydbiov1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
)

// podCacheConfig returns the cache configuration of the pods.
func podCacheConfig(t *testing.T, opts cache.Options) cache.ByObject {
	t.Helper()
	for obj, byObject := range opts.ByObject {
		if _, ok := obj.(*v1.Pod); ok {
			return byObject
		}
	}
	t.Fatal("no cache configuration found for pods")
	return cache.ByObject{}
}

func TestCacheOptions_PodSelector(t *testing.T) {
	opts := cacheOptions()
	// The other resources are read by name before they are created, so they must not be filtered.
	require.Len(t, opts.ByObject, 1)

	pods := podCacheConfig(t, opts)
	require.NotNil(t, pods.Label)
	assert.True(t, pods.Label.Matches(labels.Set{
		resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
		resources.DragonflyNameLabelKey:     "df",
		resources.RoleLabelKey:              resources.Master,
	}))
	assert.False(t, pods.Label.Matches(labels.Set{}))
	assert.False(t, pods.Label.Matches(labels.Set{resources.KubernetesAppNameLabelKey: "tekton"}))
	// Namespaces must be unset so that the watched namespaces are applied to the pods as well.
	assert.Nil(t, pods.Namespaces)
}

func TestCacheOptions_PodSelectorMatchesGeneratedPods(t *testing.T) {
	df := &dragonflydbiov1alpha1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{Name: "df", Namespace: "default"},
		Spec: dragonflydbiov1alpha1.DragonflySpec{
			Replicas: 2,
			Labels:   map[string]string{"team": "a"},
			PodMetadata: &dragonflydbiov1alpha1.MetadataSpec{
				Labels: map[string]string{"env": "test"},
			},
		},
	}

	objs, err := resources.GenerateDragonflyResources(df, "")
	require.NoError(t, err)

	selector := podCacheConfig(t, cacheOptions()).Label
	for _, obj := range objs {
		if sts, ok := obj.(*appsv1.StatefulSet); ok {
			assert.True(t, selector.Matches(labels.Set(sts.Spec.Template.Labels)))
			return
		}
	}
	t.Fatal("no statefulset generated")
}

func TestManagerCache(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set, run `make test`")
	}
	ctrl.SetLogger(logr.Discard())

	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, testEnv.Stop())
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	testLabels := map[string]string{"test": "manager-cache"}
	for _, ns := range []string{"watched", "unwatched"} {
		require.NoError(t, k8sClient.Create(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
		for name, podLabels := range map[string]map[string]string{
			"dragonfly-0": {
				resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
				resources.DragonflyNameLabelKey:     "dragonfly",
			},
			"other-0":     {resources.KubernetesAppNameLabelKey: "other"},
			"unlabeled-0": nil,
		} {
			require.NoError(t, k8sClient.Create(ctx, &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: podLabels},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{Name: "main", Image: "main"}},
				},
			}))
		}
		// The selector applies to pods only, a resource of another kind is cached without the label.
		require.NoError(t, k8sClient.Create(ctx, &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: ns, Labels: testLabels},
		}))
	}

	tests := []struct {
		name            string
		watchNamespaces string
		wantPods        []string
		wantConfigMaps  []string
	}{
		{
			name:           "all namespaces",
			wantPods:       []string{"unwatched/dragonfly-0", "watched/dragonfly-0"},
			wantConfigMaps: []string{"unwatched/config", "watched/config"},
		},
		{
			name:            "watch namespaces",
			watchNamespaces: "watched",
			wantPods:        []string{"watched/dragonfly-0"},
			wantConfigMaps:  []string{"watched/config"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgrOpts := ctrl.Options{
				Scheme:                 scheme,
				Cache:                  cacheOptions(),
				Metrics:                metricsserver.Options{BindAddress: "0"},
				HealthProbeBindAddress: "0",
			}
			require.NoError(t, addNamespacesToOpts(tt.watchNamespaces, &mgrOpts))

			mgr, err := ctrl.NewManager(cfg, mgrOpts)
			require.NoError(t, err)

			mgrCtx, stopMgr := context.WithCancel(ctx)
			stopped := make(chan error, 1)
			go func() {
				stopped <- mgr.Start(mgrCtx)
			}()
			t.Cleanup(func() {
				stopMgr()
				assert.NoError(t, <-stopped)
			})
			require.True(t, mgr.GetCache().WaitForCacheSync(mgrCtx))

			var pods v1.PodList
			require.NoError(t, mgr.GetClient().List(mgrCtx, &pods))
			var gotPods []string
			for _, pod := range pods.Items {
				gotPods = append(gotPods, pod.Namespace+"/"+pod.Name)
				assert.Empty(t, pod.ManagedFields)
			}
			assert.ElementsMatch(t, tt.wantPods, gotPods)

			var configMaps v1.ConfigMapList
			require.NoError(t, mgr.GetClient().List(mgrCtx, &configMaps, client.MatchingLabels(testLabels)))
			var gotConfigMaps []string
			for _, configMap := range configMaps.Items {
				gotConfigMaps = append(gotConfigMaps, configMap.Namespace+"/"+configMap.Name)
			}
			assert.ElementsMatch(t, tt.wantConfigMaps, gotConfigMaps)
		})
	}

	// The managed fields are only dropped from the cache.
	var pod v1.Pod
	require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: "watched", Name: "dragonfly-0"}, &pod))
	assert.NotEmpty(t, pod.ManagedFields)
}
