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
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/pkg/errors"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
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

	updatedMaster, err := clientset.CoreV1().Pods(master.Namespace).Get(ctx, master.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod %s/%s: %w", master.Namespace, master.Name, err)
	}
	master = updatedMaster

	if master.Status.Phase != corev1.PodRunning {
		return nil, fmt.Errorf("pod %s/%s is not running (phase: %s)", master.Namespace, master.Name, master.Status.Phase)
	}

	// Verify pod has Ready condition
	hasReadyCondition := false
	for _, condition := range master.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			hasReadyCondition = true
			break
		}
	}
	if !hasReadyCondition {
		return nil, fmt.Errorf("pod %s/%s is not ready (Ready condition not true)", master.Namespace, master.Name)
	}

	// Verify the Dragonfly container is ready
	containerReady := false
	for _, status := range master.Status.ContainerStatuses {
		if status.Name == resources.DragonflyContainerName {
			if status.Ready {
				containerReady = true
				break
			}
		}
	}
	if !containerReady {
		return nil, fmt.Errorf("container %s in pod %s/%s is not ready", resources.DragonflyContainerName, master.Namespace, master.Name)
	}

	fw, err := portForward(ctx, clientset, config, master, stopChan, port)
	if err != nil {
		return nil, err
	}

	redisOptions := &redis.Options{
		Addr: fmt.Sprintf("localhost:%d", port),
		MaintNotificationsConfig: &maintnotifications.Config{
			Mode: maintnotifications.ModeDisabled,
		},
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

type portForwardResult struct {
	LocalPort int
	Cleanup   func()
}

// setupPortForwardWithCleanup sets up port forwarding with proper cleanup handling
// it finds an available local port, forwards to the pod's remote port, and returns
// the local port and a cleanup function. The cleanup function must be called to
// ensure the port is released.
func setupPortForwardWithCleanup(ctx context.Context, clientset *kubernetes.Clientset, config *rest.Config, pod *corev1.Pod, remotePort int, timeout time.Duration) (*portForwardResult, error) {
	// Fetch pod to get latest status
	updatedPod, err := clientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	pod = updatedPod

	// Verify pod is running
	if pod.Status.Phase != corev1.PodRunning {
		return nil, fmt.Errorf("pod %s/%s is not running (phase: %s)", pod.Namespace, pod.Name, pod.Status.Phase)
	}

	// Verify pod has Ready condition
	hasReadyCondition := false
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			hasReadyCondition = true
			break
		}
	}
	if !hasReadyCondition {
		return nil, fmt.Errorf("pod %s/%s is not ready (Ready condition not true)", pod.Namespace, pod.Name)
	}

	// Verify the Dragonfly container is ready
	containerReady := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == resources.DragonflyContainerName {
			if status.Ready {
				containerReady = true
				break
			}
		}
	}
	if !containerReady {
		return nil, fmt.Errorf("container %s in pod %s/%s is not ready", resources.DragonflyContainerName, pod.Namespace, pod.Name)
	}

	localStopChan := make(chan struct{}, 1)
	portForwardDone := make(chan struct{}, 1)
	portForwardClosed := new(bool)

	// Port forward to pod
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

	// Find an available local port
	var localPort int
	maxPortAttempts := 10
	for i := 0; i < maxPortAttempts; i++ {
		listener, err := net.Listen("tcp", ":0")
		if err != nil {
			if i == maxPortAttempts-1 {
				return nil, fmt.Errorf("unable to find available port after %d attempts: %w", maxPortAttempts, err)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		localPort = listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		time.Sleep(100 * time.Millisecond)
		break
	}

	ports := []string{fmt.Sprintf("%d:%d", localPort, remotePort)}
	readyChan := make(chan struct{}, 1)

	fw, err := portforward.New(dialer, ports, localStopChan, readyChan, io.Discard, os.Stderr)
	if err != nil {
		return nil, err
	}

	errChan := make(chan error, 1)
	go func() {
		defer close(portForwardDone)
		errChan <- fw.ForwardPorts()
	}()

	// Wait for port forward to be ready or error
	portForwardCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case err = <-errChan:
		// Port forward failed, close and wait for cleanup
		if !*portForwardClosed && localStopChan != nil {
			close(localStopChan)
			*portForwardClosed = true
		}
		select {
		case <-portForwardDone:
		case <-time.After(2 * time.Second):
		}
		time.Sleep(500 * time.Millisecond)
		return nil, errors.Wrap(err, "unable to forward ports")
	case <-fw.Ready:
		// Port forward ready, wait a bit to establish connection
		select {
		case <-time.After(500 * time.Millisecond):
		case <-portForwardCtx.Done():
			if !*portForwardClosed && localStopChan != nil {
				close(localStopChan)
				*portForwardClosed = true
			}
			select {
			case <-portForwardDone:
			case <-time.After(2 * time.Second):
			}
			time.Sleep(500 * time.Millisecond)
			return nil, portForwardCtx.Err()
		}
	case <-portForwardCtx.Done():
		// Timeout waiting for port forward to be ready
		if !*portForwardClosed && localStopChan != nil {
			close(localStopChan)
			*portForwardClosed = true
		}
		select {
		case <-portForwardDone:
		case <-time.After(2 * time.Second):
		}
		time.Sleep(500 * time.Millisecond)
		return nil, fmt.Errorf("timeout waiting for port forward to be ready: %w", portForwardCtx.Err())
	}

	cleanup := func() {
		if !*portForwardClosed && localStopChan != nil {
			close(localStopChan)
			*portForwardClosed = true
		}
		select {
		case <-portForwardDone:
		case <-time.After(3 * time.Second):
		}
		time.Sleep(1 * time.Second)
	}

	return &portForwardResult{
		LocalPort: localPort,
		Cleanup:   cleanup,
	}, nil
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

// checkPersistenceInfo checks the persistence info from a pod's admin port
// Uses port forwarding to access the pod's admin port
// ensures container is Ready before attempting connection
func checkPersistenceInfo(ctx context.Context, clientset *kubernetes.Clientset, config *rest.Config, pod *corev1.Pod, stopChan chan struct{}) (loading string, loadState string, err error) {
	// Retry with different ports if we get port conflicts
	maxRetries := 10
	for attempt := 0; attempt < maxRetries; attempt++ {
		loading, loadState, err = tryCheckPersistenceInfo(ctx, clientset, config, pod, stopChan)
		if err == nil {
			return loading, loadState, nil
		}

		// Check if error is due to container not being ready
		errStr := err.Error()
		isNotReadyError := strings.Contains(errStr, "is not ready") ||
			strings.Contains(errStr, "not running") ||
			strings.Contains(errStr, "Ready condition not true")

		// Retry on port conflicts or container not ready
		isRetryableError := strings.Contains(errStr, "address already in use") ||
			strings.Contains(errStr, "bind") ||
			strings.Contains(errStr, "unable to forward ports") ||
			isNotReadyError

		if !isRetryableError {
			return "", "", err
		}

		if attempt < maxRetries-1 {
			// Exponential backoff, wait longer if container is not ready
			backoff := time.Duration(attempt+1) * 200 * time.Millisecond
			if isNotReadyError {
				backoff = time.Duration(attempt+1) * 1 * time.Second
			}
			cleanupWait := 2 * time.Second
			time.Sleep(backoff + cleanupWait)
		}
	}
	return "", "", err
}

func tryCheckPersistenceInfo(ctx context.Context, clientset *kubernetes.Clientset, config *rest.Config, pod *corev1.Pod, stopChan chan struct{}) (loading string, loadState string, err error) {
	// Setup port forwarding with proper cleanup
	pfResult, err := setupPortForwardWithCleanup(ctx, clientset, config, pod, resources.DragonflyAdminPort, 30*time.Second)
	if err != nil {
		return "", "", err
	}
	defer pfResult.Cleanup()

	adminClient := redis.NewClient(&redis.Options{
		Addr:                  fmt.Sprintf("localhost:%d", pfResult.LocalPort),
		DialTimeout:           15 * time.Second,
		ReadTimeout:           10 * time.Second,
		WriteTimeout:          10 * time.Second,
		ContextTimeoutEnabled: true,
	})
	defer adminClient.Close()

	infoCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	info, err := adminClient.Info(infoCtx, "persistence").Result()
	if err != nil {
		return "", "", fmt.Errorf("unable to get persistence info: %w", err)
	}

	// Parse info output
	sc := bufio.NewScanner(strings.NewReader(info))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			if key == "loading" {
				loading = value
			}
			if key == "load_state" {
				loadState = value
			}
		}
	}

	return loading, loadState, sc.Err()
}

// WriteTestResult holds the results of a continuous write test
type WriteTestResult struct {
	TotalWrites    int
	SuccessWrites  int
	FailedWrites   int
	ReadOnlyErrors int
	OtherErrors    []string
}

// runContinuousWrites performs continuous write operations against a Redis client
// and tracks success/failure counts, specifically counting READONLY errors.
// It runs for the specified duration and returns aggregate results.
func runContinuousWrites(ctx context.Context, rc *redis.Client, duration time.Duration, writeInterval time.Duration) *WriteTestResult {
	result := &WriteTestResult{
		OtherErrors: make([]string, 0),
	}

	deadline := time.Now().Add(duration)
	counter := 0

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return result
		default:
			key := fmt.Sprintf("test-key-%d", counter)
			value := fmt.Sprintf("test-value-%d-%d", counter, time.Now().UnixNano())

			result.TotalWrites++
			err := rc.Set(ctx, key, value, 0).Err()
			if err != nil {
				result.FailedWrites++
				errStr := err.Error()
				if strings.Contains(errStr, "READONLY") {
					result.ReadOnlyErrors++
				} else {
					// Limit other errors to avoid memory bloat
					if len(result.OtherErrors) < 100 {
						result.OtherErrors = append(result.OtherErrors, errStr)
					}
				}
			} else {
				result.SuccessWrites++
			}

			counter++
			time.Sleep(writeInterval)
		}
	}

	return result
}

// runContinuousWritesAsync starts continuous writes in a goroutine and returns
// a channel that will receive the result when done, plus a cancel function.
func runContinuousWritesAsync(ctx context.Context, rc *redis.Client, duration time.Duration, writeInterval time.Duration) (<-chan *WriteTestResult, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	resultChan := make(chan *WriteTestResult, 1)

	go func() {
		result := runContinuousWrites(ctx, rc, duration, writeInterval)
		resultChan <- result
		close(resultChan)
	}()

	return resultChan, cancel
}
