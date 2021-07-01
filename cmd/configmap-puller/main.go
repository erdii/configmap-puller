// Note: the example only works with the code within the same release/branch.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	kubeconfig         *string
	configmapName      *string
	configmapNamespace *string
	configmapKey       *string
	outfileName        *string
	resyncPeriod       *string
)

func main() {
	log.Println(os.Args)

	kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	configmapName = flag.String("configmap-name", "traefik-rules", "name of the configmap to watch")
	configmapNamespace = flag.String("configmap-namespace", "default", "namespace of the configmap to watch")
	configmapKey = flag.String("configmap-key", "rules.toml", "key of the configmap to read")
	outfileName = flag.String("outfile-name", "/tmp/rules.toml", "name of the file to write")
	resyncPeriod = flag.String("resync-period", "5m", "periodic duration after which a configmap resync is forced - uses https://golang.org/pkg/time/#ParseDuration under the hood")

	flag.Parse()

	log.Println("kubeconfig", *kubeconfig)
	log.Println("configmapName", *configmapName)
	log.Println("configmapNamespace", *configmapNamespace)
	log.Println("configmapKey", *configmapKey)
	log.Println("outfileName", *outfileName)
	log.Println("resyncPeriod", *resyncPeriod)
	log.Println("==========================")
	log.Println("run with --help to list all cli flags")
	log.Println("==========================")

	resyncDuration, err := time.ParseDuration(*resyncPeriod)
	if err != nil {
		panic(fmt.Errorf("not able to parse resync-period flag (given value: %s): %w", resyncDuration, err))
	}

	// load config depending if we are outside or inside a cluster
	var config *rest.Config
	if len(*kubeconfig) > 0 {
		var err error
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			panic(err.Error())
		}
	} else {
		var err error
		config, err = rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
	}

	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)

	errC, dataC, err := watchConfigMap(ctx, clientset, *configmapName, *configmapNamespace, resyncDuration)
	if err != nil {
		panic(err)
	}

	previousData := ""

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			stop()
			return
		case err := <-errC:
			panic(err)
		case data := <-dataC:
			if data == previousData {
				log.Println("data unchanged")
				continue
			}
			if err := writeFile(*outfileName, data); err != nil {
				panic(err)
			}
			log.Println("writing", "file", *outfileName)
			previousData = data
		}
	}
}

func watchConfigMap(ctx context.Context, clientset *kubernetes.Clientset, name, namespace string, resyncDuration time.Duration) (<-chan error, <-chan string, error) {
	watchSub, err := clientset.CoreV1().ConfigMaps(namespace).Watch(ctx, metav1.SingleObject(
		metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	))
	if err != nil {
		return nil, nil, err
	}

	ticker := time.NewTicker(resyncDuration)

	errChan := make(chan error)
	dataChan := make(chan string)

	go func() {
		events := watchSub.ResultChan()
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-events:
				if event.Type != watch.Added && event.Type != watch.Modified {
					continue
				}
				cm := event.Object.(*v1.ConfigMap)
				dataChan <- cm.Data[*configmapKey]

			case t := <-ticker.C:
				log.Println("tick", "t", t)
				timeoutCtx, _ := context.WithTimeout(ctx, 10*time.Second)
				cm, err := clientset.CoreV1().ConfigMaps(namespace).Get(timeoutCtx, name, metav1.GetOptions{})
				if err != nil {
					errChan <- err
					continue
				}
				dataChan <- cm.Data[*configmapKey]
			}
		}
	}()

	return errChan, dataChan, nil
}

func writeFile(filename, data string) error {
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		return fmt.Errorf("could not open file for writing: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString(data); err != nil {
		return fmt.Errorf("could not open file for writing: %w", err)
	}

	return nil
}
