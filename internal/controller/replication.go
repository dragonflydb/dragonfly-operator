package controller

import (
	"context"
	"fmt"

	"github.com/dragonflydb/dragonfly-operator/internal/resources"
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
}

// NewReplicationClient returns a new replication client
// that works with in the cluster and is the default.
func NewReplicationClient(client client.Client) *inClusterClient {
	return &inClusterClient{
		client: client,
	}
}

// replicaOf configures the pod as a replica
// to the given master instance
func (c inClusterClient) replicaOf(ctx context.Context, pod *corev1.Pod, masterIp string) error {
	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:6379", pod.Status.PodIP),
	})

	resp, err := redisClient.SlaveOf(masterIp, "6379").Result()
	if err != nil {
		return err
	}

	if resp != "OK" {
		return fmt.Errorf("Failed invoking `SLAVE OF` on the replica")
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
	redisClient := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:6379", pod.Status.PodIP),
	})

	resp, err := redisClient.SlaveOf("NO", "ONE").Result()
	if err != nil {
		return err
	}

	if resp != "OK" {
		return fmt.Errorf("Failed invoking `SLAVE OF NO ONE` on master")
	}

	pod.Labels[resources.Role] = resources.Master
	if err := c.client.Update(ctx, pod); err != nil {
		return err
	}

	return nil
}
