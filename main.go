package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var namespace, service, nodesFile string
var apiPort, peerPort int

const bootstrapVar = "INITIAL_NODELIST"

func main() {
	flag.StringVar(&namespace, "namespace", "typesense", "The namespace that Typesense is installed within")
	flag.StringVar(&service, "service", "ts", "The name of the Typesense service to use the endpoints of")
	flag.StringVar(&nodesFile, "nodes-file", "/usr/share/typesense/nodes", "The location of the file to write node information to")
	flag.IntVar(&apiPort, "api-port", 8108, "The port used by Typesense for peering")
	flag.IntVar(&peerPort, "peer-port", 8107, "The port used by Typesense for peering")
	flag.Parse()

	configPath := filepath.Join(homedir.HomeDir(), ".kube", "config")

	var config *rest.Config
	var err error

	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		// No config file found, fall back to in-cluster config.
		config, err = rest.InClusterConfig()
		if err != nil {
			log.Fatalf("failed to build local config: %s\n", err)
		}
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", configPath)
		if err != nil {
			log.Fatalf("failed to build in-cluster config: %s\n", err)
		}
	}

	clients, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("failed to create kubernetes client: %s\n", err)
	}

	watcher, err := clients.CoreV1().Endpoints(namespace).Watch(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Fatalf("failed to create endpoints watcher: %s\n", err)
	}

	initialNodes, bootstrapMode := os.LookupEnv(bootstrapVar)
	thisNode := fmt.Sprintf("%s:%d:%d", getCurrentIP(), 8107, 8108)
	if bootstrapMode {
		flattenedNodes := strings.Join([]string{initialNodes, thisNode}, ",")

		// In bootstrap mode, simply seed nodes
		log.Printf("Bootstrapping cluster with initial node list: %v", flattenedNodes)
		err = os.WriteFile(nodesFile, []byte(flattenedNodes), 0666)
		if err != nil {
			log.Fatalf("failed to write node list: %s\n", err)
		}
	}

	// Wait for Endpoint update events
	for range watcher.ResultChan() {
		nodes := getNodes(clients)
		if len(nodes) > 0 {
			flattenedNodes := strings.Join([]string{nodes, thisNode}, ",")

			log.Printf("found nodes (and added self): %s\n", flattenedNodes)
			err := os.WriteFile(nodesFile, []byte(flattenedNodes), 0666)
			if err != nil {
				log.Printf("failed to write nodes file: %s\n", err)
			}
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

	return strings.Join(nodes, ",")
}

func getCurrentIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		log.Fatalf("error resolving local addresses: %s\n", err)
	}

	for _, addr := range addrs {
		if ip, ok := addr.(*net.IPNet); ok && !ip.IP.IsLoopback() {
			if ip.IP.To4() != nil {
				return ip.IP.String()
			}
		}
	}

	return ""
}
