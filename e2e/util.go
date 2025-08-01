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
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/pkg/errors"
	"github.com/redis/go-redis/v9"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func parseTieredEntriesFromInfo(info string) (int64, error) {
	sc := bufio.NewScanner(strings.NewReader(info))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Handle "tiered_entries:<number>" (with optional spaces)
		if strings.HasPrefix(line, "tiered_entries") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				val := strings.TrimSpace(parts[1])
				return strconv.ParseInt(val, 10, 64)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("tiered_entries not found")
}

func waitForStatefulSetReady(ctx context.Context, c client.Client, name, namespace string, maxDuration time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for statefulset to be ready")
		default:
			// Check if the statefulset is ready
			ready, err := isStatefulSetReady(ctx, c, name, namespace)
			if err != nil {
				return err
			}
			if ready {
				return nil
			}
		}
	}
}

func isStatefulSetReady(ctx context.Context, c client.Client, name, namespace string) (bool, error) {
	var statefulSet appsv1.StatefulSet
	if err := c.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, &statefulSet); err != nil {
		return false, nil
	}

	if statefulSet.Status.ReadyReplicas == *statefulSet.Spec.Replicas && statefulSet.Status.UpdatedReplicas == statefulSet.Status.Replicas {
		return true, nil
	}

	return false, nil
}

func checkAndK8sPortForwardRedis(ctx context.Context, clientset *kubernetes.Clientset, config *rest.Config, stopChan chan struct{}, name, namespace, password string, port int) (*redis.Client, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", name),
	})
	if err != nil {
		return nil, err
	}

	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found")
	}

	var master *corev1.Pod
	for _, pod := range pods.Items {
		if pod.Labels[resources.RoleLabelKey] == resources.Master {
			master = &pod
			break
		}
	}

	if master == nil {
		return nil, fmt.Errorf("no master pod found")
	}

	fw, err := portForward(ctx, clientset, config, master, stopChan, port)
	if err != nil {
		return nil, err
	}

	redisOptions := &redis.Options{
		Addr: fmt.Sprintf("localhost:%d", port),
	}

	if password != "" {
		redisOptions.Password = password
	}

	redisClient := redis.NewClient(redisOptions)

	errChan := make(chan error, 1)
	go func() { errChan <- fw.ForwardPorts() }()

	select {
	case err = <-errChan:
		return nil, errors.Wrap(err, "unable to forward ports")
	case <-fw.Ready:
	}

	pingCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	err = redisClient.Ping(pingCtx).Err()
	if err != nil {
		return nil, fmt.Errorf("unable to ping instance: %w", err)
	}

	return redisClient, nil
}

func portForward(ctx context.Context, clientset *kubernetes.Clientset, config *rest.Config, pod *corev1.Pod, stopChan chan struct{}, port int) (*portforward.PortForwarder, error) {
	url := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("portforward").
		URL()

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return nil, err
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", url)
	ports := []string{fmt.Sprintf("%d:%d", port, resources.DragonflyPort)}
	readyChan := make(chan struct{}, 1)

	fw, err := portforward.New(dialer, ports, stopChan, readyChan, io.Discard, os.Stderr)
	if err != nil {
		return nil, err
	}
	return fw, err
}
