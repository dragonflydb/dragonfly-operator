package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-redis/redis"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/client-go/util/homedir"
)

func main() {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	dbClient := flag.Arg(0)

	if dbClient == "" {
		panic("No dragonfly object specified")
	}

	pods, err := clientset.CoreV1().Pods("default").List(context.TODO(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=%s", dbClient),
	})
	if err != nil {
		panic(err.Error())
	}

	startPort := 6379

	for i, pod := range pods.Items {
		err := portForward(context.Background(), clientset, config, pod, startPort+i)
		if err != nil {
			panic(err.Error())
		}

		role, err := getRole(fmt.Sprintf("localhost:%d", startPort+i))
		if err != nil {
			panic(err.Error())
		}

		fmt.Printf("%s, %s: %s\n", pod.Name, pod.Status.PodIP, role)
	}
}

func portForward(ctx context.Context, clientset *kubernetes.Clientset, config *rest.Config, pod corev1.Pod, port int) error {

	url := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(pod.Namespace).
		Name(pod.Name).
		SubResource("portforward").
		URL()

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return err
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", url)
	ports := []string{fmt.Sprintf("%d:%d", port, 6379)}
	readyChan := make(chan struct{}, 1)
	stopChan := make(chan struct{}, 1)

	fw, err := portforward.New(dialer, ports, stopChan, readyChan, io.Discard, os.Stderr)
	if err != nil {
		return err
	}

	errChan := make(chan error, 1)
	go func() { errChan <- fw.ForwardPorts() }()

	select {
	case err = <-errChan:
		return errors.Wrap(err, "port forwarding failed")
	case <-fw.Ready:
	}

	return nil
}

func getRole(url string) (string, error) {
	redisClient := redis.NewClient(&redis.Options{
		Addr: url,
	})

	resp, err := redisClient.Info("replication", "role").Result()
	if err != nil {
		return "", err
	}

	return resp, nil
}
