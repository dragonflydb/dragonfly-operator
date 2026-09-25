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

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/controller"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/redis/go-redis/v9"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe("DF Flush On Failover", Ordered, FlakeAttempts(3), func() {
	ctx := context.Background()
	name := "flush-on-failover-test"
	namespace := "default"
	replicas := 2

	disabled := false
	df := dfv1alpha1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: dfv1alpha1.DragonflySpec{
			Replicas:             int32(replicas),
			NetworkPolicyEnabled: &disabled,
			FlushOnFailover:      true,
		},
	}

	Context("A promoted master is flushed", func() {
		It("Instance is created and ready", func() {
			Expect(k8sClient.Create(ctx, &df)).To(BeNil())

			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 3*time.Minute)).To(BeNil())
			Expect(waitForStatefulSetReady(ctx, k8sClient, name, namespace, 2*time.Minute)).To(BeNil())
			Expect(waitForMasterPod(ctx, k8sClient, name, namespace, 2*time.Minute)).To(BeNil())
		})

		It("Data replicated before the failover is dropped on the promoted master", func() {
			// Write to the current master.
			stopChan := make(chan struct{}, 1)
			rc, err := checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6397)
			Expect(err).To(BeNil())
			Expect(rc.Set(ctx, "foo", "bar", 0).Err()).To(BeNil())
			close(stopChan)
			rc.Close()

			master, replica, err := getMasterReplica(ctx, namespace, name)
			Expect(err).To(BeNil())

			// The replica has to actually hold the data before the master is killed, otherwise an
			// empty dataset after the promotion would prove nothing.
			replicaPF, err := setupPortForwardWithCleanup(ctx, clientset, cfg, replica, resources.DragonflyPort, 30*time.Second)
			Expect(err).To(BeNil())
			replicaClient := redis.NewClient(&redis.Options{
				Addr: fmt.Sprintf("localhost:%d", replicaPF.LocalPort),
			})
			Eventually(func() (string, error) {
				return replicaClient.Get(ctx, "foo").Result()
			}, 1*time.Minute, 2*time.Second).Should(Equal("bar"))
			replicaClient.Close()
			replicaPF.Cleanup()

			// Kill the master: the operator promotes the replica with SLAVE OF NO ONE and, with
			// flushOnFailover enabled, flushes it before labelling it as the new master.
			Expect(k8sClient.Delete(ctx, master)).To(BeNil())

			Expect(waitForMasterPod(ctx, k8sClient, name, namespace, 2*time.Minute)).To(BeNil())
			Expect(waitForDragonflyPhase(ctx, k8sClient, name, namespace, controller.PhaseReady, 2*time.Minute)).To(BeNil())

			// The pod that was a replica is the one that got promoted.
			Eventually(func() (string, error) {
				newMaster, _, err := getMasterReplica(ctx, namespace, name)
				if err != nil {
					return "", err
				}
				return newMaster.Name, nil
			}, 2*time.Minute, 5*time.Second).Should(Equal(replica.Name))

			// And it serves an empty dataset.
			stopChan = make(chan struct{}, 1)
			rc, err = checkAndK8sPortForwardRedis(ctx, clientset, cfg, stopChan, name, namespace, "", 6398)
			Expect(err).To(BeNil())
			defer close(stopChan)
			defer rc.Close()

			Expect(rc.DBSize(ctx).Result()).To(BeEquivalentTo(0))
		})

		It("Cleanup", func() {
			var df dfv1alpha1.Dragonfly
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, &df)).To(BeNil())

			Expect(k8sClient.Delete(ctx, &df)).To(BeNil())
		})
	})
})
