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
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dfv1alpha1 "github.com/dragonflydb/dragonfly-operator/api/v1alpha1"
	"github.com/dragonflydb/dragonfly-operator/internal/resources"
	"github.com/go-logr/logr"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	demotedPodIP = "10.42.0.7"
	newMasterIP  = "10.42.0.8"
)

// A client reconnecting while the demoted pod is still an endpoint lands back on
// the read-only instance, so demotion must not disconnect anyone yet.
func TestReplicaOfDoesNotDisconnectClientsWhileStillInTheMasterService(t *testing.T) {
	k8s := newStaleEndpointClient(buildDemotionFixture(t))
	srv := startFakeDragonfly(t, k8s.podInEndpoints)

	dfi := newDemotionInstance(k8s, srv.addr())
	defer dfi.Close()

	pod := getDemotionPod(t, k8s)
	require.NoError(t, dfi.replicaOf(t.Context(), pod, newMasterIP))

	assert.False(t, srv.killedWhileInEndpoints(),
		"clients were killed while the demoted pod was still an endpoint of the master Service: "+
			"a reconnect in that window resolves back to the read-only instance")
}

func getDemotionPod(t *testing.T, k8s client.Client) *corev1.Pod {
	t.Helper()

	pod := &corev1.Pod{}
	require.NoError(t, k8s.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "df-0"}, pod))

	return pod
}

// buildDemotionFixture returns a master pod that is the sole endpoint of its service.
func buildDemotionFixture(t *testing.T) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, discoveryv1.AddToScheme(scheme))
	require.NoError(t, dfv1alpha1.AddToScheme(scheme))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "df-0",
			Namespace: "default",
			Labels: map[string]string{
				resources.DragonflyNameLabelKey:     "df",
				resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
				resources.KubernetesPartOfLabelKey:  resources.KubernetesPartOf,
				resources.RoleLabelKey:              resources.Master,
			},
		},
		Status: corev1.PodStatus{PodIP: demotedPodIP},
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "df", Namespace: "default"},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				resources.DragonflyNameLabelKey:     "df",
				resources.KubernetesAppNameLabelKey: resources.KubernetesAppName,
				resources.RoleLabelKey:              resources.Master,
			},
		},
	}

	ready := true
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "df-xk29p",
			Namespace: "default",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "df"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{demotedPodIP},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Name: "df-0", Namespace: "default"},
		}},
	}

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod, service, slice).Build()
}

func newDemotionInstance(k8s client.Client, redisAddr string) *DragonflyInstance {
	return &DragonflyInstance{
		df: &dfv1alpha1.Dragonfly{
			ObjectMeta: metav1.ObjectMeta{Name: "df", Namespace: "default"},
		},
		client: k8s,
		log:    logr.Discard(),
		redisClients: map[string]*redis.Client{
			demotedPodIP: redis.NewClient(&redis.Options{
				ClientName:  resources.DragonflyOperatorName,
				Addr:        redisAddr,
				DialTimeout: 5 * time.Second,
			}),
		},
	}
}

// staleEndpointClient models the lag of the EndpointSlice controller: only the
// first read still shows the demoted pod.
type staleEndpointClient struct {
	client.Client

	// neverConverges stands in for a stalled EndpointSlice controller.
	neverConverges bool

	mu    sync.Mutex
	reads int
}

func newStaleEndpointClient(c client.Client) *staleEndpointClient {
	return &staleEndpointClient{Client: c}
}

func (c *staleEndpointClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	slices, ok := list.(*discoveryv1.EndpointSliceList)
	if !ok {
		return c.Client.List(ctx, list, opts...)
	}

	if err := c.Client.List(ctx, slices, opts...); err != nil {
		return err
	}

	c.mu.Lock()
	c.reads++
	stale := c.neverConverges || c.reads == 1
	c.mu.Unlock()

	if !stale {
		for i := range slices.Items {
			slices.Items[i].Endpoints = nil
		}
	}
	return nil
}

// podInEndpoints reports whether a reconnect would still land on the demoted pod.
func (c *staleEndpointClient) podInEndpoints() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.neverConverges || c.reads < 2
}

// fakeDragonfly is a minimal RESP server that records when clients were killed.
type fakeDragonfly struct {
	listener net.Listener

	podInEndpoints func() bool

	mu sync.Mutex
	// failDisconnect makes CLIENT KILL fail, as a timeout on a busy instance would.
	failDisconnect bool
	killed         bool
	killedInEndp   bool
	attempts       int
}

func startFakeDragonfly(t *testing.T, podInEndpoints func() bool) *fakeDragonfly {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := &fakeDragonfly{listener: listener, podInEndpoints: podInEndpoints}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go srv.serve(conn)
		}
	}()

	return srv
}

func (s *fakeDragonfly) addr() string { return s.listener.Addr().String() }

func (s *fakeDragonfly) sawKill() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.killed
}

func (s *fakeDragonfly) killedWhileInEndpoints() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.killedInEndp
}

func (s *fakeDragonfly) disconnectAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

func (s *fakeDragonfly) setFailDisconnect(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failDisconnect = fail
}

// startDisconnect records the CLIENT KILL attempt and reports whether it succeeds.
func (s *fakeDragonfly) startDisconnect() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	return !s.failDisconnect
}

func (s *fakeDragonfly) recordKill() {
	inEndpoints := s.podInEndpoints()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.killed = true
	s.killedInEndp = s.killedInEndp || inEndpoints
}

func (s *fakeDragonfly) serve(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	for {
		args, err := readRESPCommand(reader)
		if err != nil {
			return
		}
		if _, err := conn.Write(s.reply(args)); err != nil {
			return
		}
	}
}

func (s *fakeDragonfly) reply(args []string) []byte {
	cmd := strings.ToLower(args[0])
	sub := ""
	if len(args) > 1 {
		sub = strings.ToLower(args[1])
	}

	switch {
	case cmd == "hello":
		// go-redis tolerates a redis error here and falls back to RESP2.
		return []byte("-ERR unknown command 'HELLO'\r\n")
	case cmd == "info":
		return respBulk("# Replication\r\nrole:master\r\nconnected_slaves:1\r\n")
	case cmd == "client" && sub == "kill":
		if !s.startDisconnect() {
			return []byte("-ERR simulated failure\r\n")
		}
		s.recordKill()
		// Dragonfly answers with the number of killed connections.
		return []byte(":1\r\n")
	default:
		return []byte("+OK\r\n")
	}
}

func respBulk(payload string) []byte {
	return []byte("$" + strconv.Itoa(len(payload)) + "\r\n" + payload + "\r\n")
}

func readRESPCommand(reader *bufio.Reader) ([]string, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	header = strings.TrimRight(header, "\r\n")
	if !strings.HasPrefix(header, "*") {
		return nil, fmt.Errorf("expected a RESP array, got %q", header)
	}
	count, err := strconv.Atoi(header[1:])
	if err != nil || count < 1 {
		return nil, fmt.Errorf("bad RESP array header %q", header)
	}

	args := make([]string, 0, count)
	for range count {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(strings.TrimRight(lengthLine, "\r\n")[1:])
		if err != nil {
			return nil, fmt.Errorf("bad RESP bulk header %q", lengthLine)
		}
		buf := make([]byte, length+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:length]))
	}
	return args, nil
}
