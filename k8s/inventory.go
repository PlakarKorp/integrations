package k8s

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	_ "embed"

	sdk "github.com/PlakarKorp/go-inventory-sdk/inventory"
	"github.com/PlakarKorp/pkg"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

//go:embed plugin/inventory/k8s.json
var Schema []byte

type inventory struct {
	namespaces []string
	config     *rest.Config
	clientset  kubernetes.Interface
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
		case "k8s_filter_namespaces":
			for ns := range strings.SplitSeq(v, ",") {
				ns = strings.TrimSpace(ns)
				if ns == "" {
					continue
				}
				inv.namespaces = append(inv.namespaces, ns)
			}
			slices.Sort(inv.namespaces)
			inv.namespaces = slices.Compact(inv.namespaces)
			if len(inv.namespaces) == 0 {
				inv.namespaces = append(inv.namespaces, "")
			}
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
	for _, ns := range inv.namespaces {
		if err := inv.listPVCInNs(ctx, ns, resources); err != nil {
			return err
		}
	}
	return nil
}

func (inv *inventory) listPVCInNs(ctx context.Context, ns string, resources chan<- *sdk.InventoryEntry) error {
	var cont string

	for {
		pvcs, err := inv.clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{
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
					Endpoint: "/" + pvc.Namespace + "/" + pvc.Name,
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
