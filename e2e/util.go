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
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

	if statefulSet.Status.ReadyReplicas == *statefulSet.Spec.Replicas {
		return true, nil
	}

	return false, nil
}

type RunCmd struct {
	*redis.Client
	*portforward.PortForwarder
}

func InitRunCmd(ctx context.Context, stopChan chan struct{}, name, namespace, password string) (*RunCmd, error) {
	home := homedir.HomeDir()
	if home == "" {
		return nil, fmt.Errorf("can't find kube-config")
	}
	kubeconfig := filepath.Join(home, ".kube", "config")
	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, err
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", name),
	})
	if err != nil {
		return nil, err
	}

	var master *corev1.Pod
	for _, pod := range pods.Items {
		if pod.Labels[resources.Role] == resources.Master {
			master = &pod
			break
		}
	}

	fw, err := portForward(ctx, clientset, config, master, stopChan, resources.DragonflyPort)
	if err != nil {
		return nil, err
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("localhost:%d", resources.DragonflyPort),
		Password: password,
	})
	return &RunCmd{Client: redisClient, PortForwarder: fw}, nil
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

func (r *RunCmd) Start(ctx context.Context) error {
	errChan := make(chan error, 1)
	var err error
	go func() { errChan <- r.ForwardPorts() }()

	select {
	case err = <-errChan:
		return errors.Wrap(err, "port forwarding failed")
	case <-r.Ready:
	}

	pingCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	err = r.Ping(pingCtx).Err()
	if err != nil {
		return fmt.Errorf("unable to ping instance")
	}
	return nil
}
