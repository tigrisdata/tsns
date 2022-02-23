package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var appSelector, namespace, nodesFile, service string
var apiPort, peerPort int

func main() {
	flag.StringVar(&appSelector, "app-selector", "app.kubernetes.io/name=typesense", "Full label expression used to list Typesense pods")
	flag.StringVar(&namespace, "namespace", "typesense", "The namespace that Typesense is installed within")
	flag.StringVar(&service, "service", "ts", "The name of the Typesense service to use the endpoints of")
	flag.StringVar(&nodesFile, "nodes-file", "/usr/share/typesense/nodes", "The location of the file to write node information to")
	flag.IntVar(&apiPort, "api-port", 8108, "The port used by Typesense for API requests")
	flag.IntVar(&peerPort, "peer-port", 8107, "The port used by Typesense for peering")
	flag.Parse()

	configPath := filepath.Join(homedir.HomeDir(), ".kube", "config")

	var config *rest.Config
	var err error

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		// No config file found, fall back to in-cluster config.
		config, err = rest.InClusterConfig()
		if err != nil {
			log.Printf("failed to build local config: %s\n", err)
		}
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", configPath)
		if err != nil {
			log.Printf("failed to build in-cluster config: %s\n", err)
		}
	}

	clients, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("failed to create kubernetes client: %s\n", err)
	}

	watcher, err := clients.CoreV1().Endpoints(namespace).Watch(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Printf("failed to create endpoints watcher: %s\n", err)
	}

	for range watcher.ResultChan() {
		err := os.WriteFile(nodesFile, []byte(getNodes(clients)), 0666)
		if err != nil {
			log.Printf("failed to write nodes file: %s\n", err)
		}
	}
}

func getNodes(clients *kubernetes.Clientset) string {
	var nodes []string

	endpoints, err := clients.CoreV1().Endpoints(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Printf("failed to list endpoints: %s\n", err)
		return ""
	}

	for _, e := range endpoints.Items {
		if e.Name != service {
			continue
		}

		for _, s := range e.Subsets {
			for _, a := range s.Addresses {
				nodes = append(nodes, fmt.Sprintf("%s:%d:%d", a.IP, peerPort, apiPort))
			}
		}
	}

	// Fall back to grabbing the pod IPs, we should only need to do this if the pods aren't ready,
	// e.g. if we're bootstrapping a cluster.
	if len(nodes) == 0 {
		pods, err := clients.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{
			LabelSelector: appSelector,
		})

		if err != nil {
			log.Printf("failed to list pods: %s\n", err)
			return ""
		}

		for _, pod := range pods.Items {
			nodes = append(nodes, fmt.Sprintf("%s:%d:%d", pod.Status.PodIP, peerPort, apiPort))
		}
	}

	return strings.Join(nodes, ",")
}