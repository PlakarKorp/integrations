package k8s

import (
	"context"
	"fmt"
	"os"

	_ "embed"

	sdk "github.com/PlakarKorp/go-inventory-sdk/inventory"
	"github.com/PlakarKorp/pkg"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type inventory struct {
	config    *rest.Config
	clientset *kubernetes.Clientset
}

func NewInventory(ctx context.Context, params map[string]string) (sdk.Inventory, error) {
	var (
		inv          inventory
		kubeconf     []byte
		kubeconfpath string
	)

	for k, v := range params {
		switch k {
		case "k8s_kubeconf":
			kubeconf = []byte(v)
		case "k8s_kubeconf_path":
			// this is just for ease of development
			kubeconfpath = v
		}
	}

	if len(kubeconf) == 0 && kubeconfpath != "" {
		var err error
		kubeconf, err = os.ReadFile(kubeconfpath)
		if err != nil {
			return nil, fmt.Errorf("failed to open %s: %w",
				kubeconfpath, err)
		}
	}

	if kubeconf != nil {
		config, err := clientcmd.RESTConfigFromKubeConfig(kubeconf)
		if err != nil {
			return nil, fmt.Errorf("failed to create config from kubeconfig: %w", err)
		}
		inv.config = config
	} else {
		config, err := rest.InClusterConfig()
		if err != nil {
			return nil, err
		}
		inv.config = config
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(inv.config)
	if err != nil {
		return nil, err
	}
	inv.clientset = clientset

	return &inv, nil
}

func (inv *inventory) listPVC(ctx context.Context, resources chan<- *sdk.InventoryEntry) error {
	var cont string

	for {
		pvcs, err := inv.clientset.CoreV1().PersistentVolumeClaims("").List(ctx, metav1.ListOptions{
			Limit:    50,
			Continue: cont,
		})
		if err != nil {
			return fmt.Errorf("failed to list PVCs: %w", err)
		}

		for _, pvc := range pvcs.Items {
			resources <- &sdk.InventoryEntry{
				Class:    pkg.ResourceClassBlockStorage,
				SubClass: pkg.ResourceSubClassPVC,
				URN:      "k8s:" + pvc.Namespace + ":" + pvc.Name,
				Name:     pvc.Name,
				Endpoints: []sdk.HostEndpoint{{
					Type:     sdk.EndpointIdentifier,
					Endpoint: pvc.Namespace + "/" + pvc.Name,
				}},
			}
		}

		cont = pvcs.Continue
		if cont == "" {
			break
		}
	}

	return nil
}

func (inv *inventory) List(ctx context.Context, resources chan<- *sdk.InventoryEntry) error {
	defer close(resources)

	if err := inv.listPVC(ctx, resources); err != nil {
		return err
	}

	return nil
}

func (inv *inventory) Close(ctx context.Context) error {
	return nil
}
