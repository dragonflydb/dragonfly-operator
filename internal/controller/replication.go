package controller

import (
	"context"
	"fmt"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/go-logr/logr"
	"github.com/go-redis/redis"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type replicationClient interface {
	replicaOf(ctx context.Context, pod *corev1.Pod, masterIp string) error
	replicaOfNoOne(ctx context.Context, pod *corev1.Pod) error
}

type inClusterClient struct {
	client client.Client
	log    logr.Logger
}

// NewReplicationClient returns a new replication client
// that works with in the cluster and is the default.
func NewReplicationClient(client client.Client, log logr.Logger) *inClusterClient {
	return &inClusterClient{
		client: client,
		log:    log,
	}
}

// replicaOf configures the pod as a replica
// to the given master instance
func (c inClusterClient) replicaOf(ctx context.Context, pod *corev1.Pod, masterIp string) error {

	// retry logic as port-forwarding is not reliable in CI
	var stopChan chan struct{}
	var err error
	var resp string
	func() {
		for i := 0; i < 5; i++ {
			redisClient := redis.NewClient(&redis.Options{
				Addr: fmt.Sprintf("%s:6379", pod.Status.PodIP),
			})

			c.log.Info("Trying to invoke SLAVE OF command", "pod", pod.Name, "master", masterIp)
			resp, err = redisClient.SlaveOf(masterIp, "6379").Result()
			if err != nil {
				continue
			}

			// Close Channel
			stopChan <- struct{}{}

			if resp == "OK" {
				return
			}
		}
	}()
	if err != nil {
		c.log.Error(err, "Error running SLAVE OF command", "pod", pod.Name)
		return err
	}

	if resp != "OK" {
		c.log.Error(err, "Response of `SLAVE OF` on master is not OK", "response", resp)
		return fmt.Errorf("Response of `SLAVE OF` on replica is not OK")
	}

	pod.Labels[resources.Role] = resources.Replica
	if err := c.client.Update(ctx, pod); err != nil {
		return fmt.Errorf("could not update replica label")
	}

	return nil
}

// replicaOfNoOne configures the pod as a master
// along while updating other pods to be replicas
func (c inClusterClient) replicaOfNoOne(ctx context.Context, pod *corev1.Pod) error {
	// retry logic as command issuance can timeout and fail
	var stopChan chan struct{}
	var err error
	var resp string
	func() {
		for i := 0; i < 5; i++ {
			redisClient := redis.NewClient(&redis.Options{
				Addr: fmt.Sprintf("%s:6379", pod.Status.PodIP),
			})

			c.log.Info("Running SLAVE OF NO ONE command", "pod", pod.Name)
			resp, err = redisClient.SlaveOf("NO", "ONE").Result()
			if err != nil {
				continue
			}

			// Close Channel
			stopChan <- struct{}{}

			if resp == "OK" {
				return
			}
		}
	}()
	if err != nil {
		c.log.Error(err, "Error running SLAVE OF NO ONE command", "pod", pod.Name)
		return err
	}

	if resp != "OK" {
		return fmt.Errorf("Response of `SLAVE OF NO NE` on master is not OK")
	}

	pod.Labels[resources.Role] = resources.Master
	if err := c.client.Update(ctx, pod); err != nil {
		return err
	}

	return nil
}
