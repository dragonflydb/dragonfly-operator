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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type conflictUpdateClient struct {
	client.Client
	updateCalled bool
}

func (c *conflictUpdateClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	c.updateCalled = true
	return apierrors.NewConflict(schema.GroupResource{Group: "", Resource: "pods"}, obj.GetName(), fmt.Errorf("conflict"))
}

type respServer struct {
	ln     net.Listener
	wg     sync.WaitGroup
	closed chan struct{}
}

func newRespServer(t *testing.T, addr string) *respServer {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("failed to listen on %s: %v", addr, err)
	}
	s := &respServer{
		ln:     ln,
		closed: make(chan struct{}),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s
}

func (s *respServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				continue
			}
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *respServer) Close() {
	close(s.closed)
	_ = s.ln.Close()
	s.wg.Wait()
}

func (s *respServer) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	for {
		cmd, err := readCommand(reader)
		if err != nil {
			return
		}
		if len(cmd) == 0 {
			continue
		}
		switch strings.ToUpper(cmd[0]) {
		case "HELLO":
			// RESP3 map response expected by go-redis
			writeResp3Map(writer, map[string]interface{}{
				"server":  "redis",
				"version": "6.2.0",
				"proto":   3,
			})
		case "PING":
			writeSimpleString(writer, "PONG")
		case "INFO":
			// Return master role for hasMasterRole()
			writeBulkString(writer, "role:master\r\n")
		case "SLAVEOF":
			writeSimpleString(writer, "OK")
		case "CLIENT":
			if len(cmd) >= 2 {
				switch strings.ToUpper(cmd[1]) {
				case "LIST":
					writeBulkString(writer, "")
				case "KILL", "SETNAME":
					writeSimpleString(writer, "OK")
				default:
					writeSimpleString(writer, "OK")
				}
			} else {
				writeSimpleString(writer, "OK")
			}
		case "QUIT":
			writeSimpleString(writer, "OK")
			return
		default:
			writeSimpleString(writer, "OK")
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func readCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, fmt.Errorf("empty line")
	}
	if line[0] != '*' {
		return nil, fmt.Errorf("unexpected RESP type: %q", line)
	}
	count, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lenLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if len(lenLine) == 0 || lenLine[0] != '$' {
			return nil, fmt.Errorf("expected bulk string length")
		}
		n, err := strconv.Atoi(strings.TrimSpace(lenLine[1:]))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:n]))
	}
	return args, nil
}

func writeSimpleString(writer *bufio.Writer, s string) {
	_, _ = writer.WriteString("+" + s + "\r\n")
}

func writeBulkString(writer *bufio.Writer, s string) {
	_, _ = writer.WriteString("$" + strconv.Itoa(len(s)) + "\r\n" + s + "\r\n")
}

func writeResp3Map(writer *bufio.Writer, values map[string]interface{}) {
	_, _ = writer.WriteString("%" + strconv.Itoa(len(values)) + "\r\n")
	for k, v := range values {
		writeSimpleString(writer, k)
		switch typed := v.(type) {
		case int:
			_, _ = writer.WriteString(":" + strconv.Itoa(typed) + "\r\n")
		case string:
			writeSimpleString(writer, typed)
		default:
			writeSimpleString(writer, fmt.Sprintf("%v", v))
		}
	}
}

func TestReplicaOf_PatchAvoidsResourceVersionConflict(t *testing.T) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(resources.DragonflyAdminPort))
	server := newRespServer(t, addr)
	defer server.Close()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add corev1 scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add appsv1 scheme: %v", err)
	}
	if err := dfv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add dragonfly scheme: %v", err)
	}

	df := &dfv1alpha1.Dragonfly{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "df-test",
			Namespace: "default",
		},
		Spec: dfv1alpha1.DragonflySpec{
			Replicas: 2,
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "df-test-0",
			Namespace: "default",
			Labels: map[string]string{
				resources.RoleLabelKey: resources.Master,
			},
		},
		Status: corev1.PodStatus{
			PodIP: "127.0.0.1",
		},
	}

	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(df, pod).
		Build()
	wrappedClient := &conflictUpdateClient{Client: baseClient}

	dfi := &DragonflyInstance{
		df:     df,
		client: wrappedClient,
		log:    logr.Discard(),
		scheme: scheme,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := dfi.replicaOf(ctx, pod, "10.0.0.2"); err != nil {
		t.Fatalf("replicaOf failed: %v", err)
	}

	if wrappedClient.updateCalled {
		t.Fatalf("expected replicaOf to avoid Update, but Update was called")
	}

	var updatedPod corev1.Pod
	if err := baseClient.Get(ctx, client.ObjectKey{Name: pod.Name, Namespace: pod.Namespace}, &updatedPod); err != nil {
		t.Fatalf("failed to fetch pod: %v", err)
	}
	if updatedPod.Labels[resources.RoleLabelKey] != resources.Replica {
		t.Fatalf("expected role label to be replica, got %q", updatedPod.Labels[resources.RoleLabelKey])
	}
	if updatedPod.Labels[resources.TrafficLabelKey] != resources.TrafficDisabled {
		t.Fatalf("expected traffic label to be disabled, got %q", updatedPod.Labels[resources.TrafficLabelKey])
	}
}
