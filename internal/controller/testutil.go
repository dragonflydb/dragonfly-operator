package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/go-redis/redis"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type outClusterClient struct {
	clientset *kubernetes.Clientset
	config    rest.Config
}

func newOutClusterReplicationClient(clientset *kubernetes.Clientset, config rest.Config) *outClusterClient {
	return &outClusterClient{
		clientset: clientset,
		config:    config,
	}
}

// replicaOf configures the pod as a replica
// to the given master instance
func (c outClusterClient) replicaOf(ctx context.Context, pod *corev1.Pod, masterIp string) error {
	err, stopChan := portForward(context.Background(), c.clientset, &c.config, pod, 6379)
	if err != nil {
		return err
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:6379", "localhost"),
	})

	resp, err := redisClient.SlaveOf(masterIp, "6379").Result()
	if err != nil {
		return err
	}

	// Close Channel
	stopChan <- struct{}{}

	if resp != "OK" {
		return fmt.Errorf("Failed invoking `SLAVE OF` on the replica")
	}

	pod.Labels[resources.Role] = resources.Replica
	if _, err := c.clientset.CoreV1().Pods(pod.Namespace).Update(ctx, pod, v1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}

// replicaOfNoOne configures the pod as a master
// along while updating other pods to be replicas
func (c outClusterClient) replicaOfNoOne(ctx context.Context, pod *corev1.Pod) error {
	err, stopChan := portForward(context.Background(), c.clientset, &c.config, pod, 6379)
	if err != nil {
		return err
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:6379", "localhost"),
	})

	resp, err := redisClient.SlaveOf("NO", "ONE").Result()
	if err != nil {
		return err
	}

	// Close Channel
	stopChan <- struct{}{}

	if resp != "OK" {
		return fmt.Errorf("Failed invoking `SLAVE OF NO ONE` on master")
	}

	pod.Labels[resources.Role] = resources.Master
	if _, err := c.clientset.CoreV1().Pods(pod.Namespace).Update(ctx, pod, v1.UpdateOptions{}); err != nil {
		return err
	}

	return nil
}

func portForward(ctx context.Context, clientset *kubernetes.Clientset, config *rest.Config, pod *corev1.Pod, port int) (error, chan struct{}) {
	url := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("portforward").
		URL()

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return err, nil
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", url)
	ports := []string{fmt.Sprintf("%d:%d", port, 6379)}
	readyChan := make(chan struct{}, 1)
	stopChan := make(chan struct{}, 1)

	fw, err := portforward.New(dialer, ports, stopChan, readyChan, io.Discard, os.Stderr)
	if err != nil {
		return err, nil
	}

	errChan := make(chan error, 1)
	go func() { errChan <- fw.ForwardPorts() }()

	select {
	case err = <-errChan:
		return errors.Wrap(err, "port forwarding failed"), nil
	case <-fw.Ready:
	}

	return nil, stopChan
}
