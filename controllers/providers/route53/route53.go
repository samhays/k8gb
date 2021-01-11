package route53

import (
	"context"
	"encoding/json"
	coreerrors "errors"
	"fmt"
	"os"

	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	"github.com/AbsaOSS/k8gb/controllers/depresolver"
	"github.com/AbsaOSS/k8gb/controllers/internal/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	externaldns "sigs.k8s.io/external-dns/endpoint"

	"sort"
	"strings"
)

const (
	providerName          = "route53"
	coreDNSExtServiceName = "k8gb-coredns-lb"
)

var log = logf.Log.WithName("controller_gslb")

// TODO: move to depresolver
var k8gbNamespace = os.Getenv("POD_NAMESPACE")

type Route53 struct {
	gslb   *k8gbv1beta1.Gslb
	config *depresolver.Config
	client client.Client
}

func NewRoute53(config *depresolver.Config, gslb *k8gbv1beta1.Gslb, client client.Client) (i *Route53, err error) {
	if gslb == nil {
		return i, fmt.Errorf("nil *Gslb")
	}
	if config == nil {
		return i, fmt.Errorf("nil *Config")
	}
	i = &Route53{
		gslb:   gslb,
		config: config,
		client: client,
	}
	return
}

//TODO: reuse
func (n *Route53) ConfigureZoneDelegation() (r *reconcile.Result, err error) {
	ttl := externaldns.TTL(n.gslb.Spec.Strategy.DNSTtlSeconds)
	log.Info(fmt.Sprintf("Creating/Updating DNSEndpoint CRDs for %s...", providerName))
	var NSServerList []string
	NSServerList = append(NSServerList, n.nsServerName())
	NSServerList = append(NSServerList, utils.NsServerNameExt(n.config.DNSZone, n.config.EdgeDNSZone, n.config.ExtClustersGeoTags)...)
	sort.Strings(NSServerList)
	NSServerIPs, err := n.coreDNSExposedIPs()
	if err != nil {
		return &reconcile.Result{}, err
	}
	NSRecord := &externaldns.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:        fmt.Sprintf("k8gb-ns-%s", providerName),
			Namespace:   k8gbNamespace,
			Annotations: map[string]string{"k8gb.absa.oss/dnstype": providerName},
		},
		Spec: externaldns.DNSEndpointSpec{
			Endpoints: []*externaldns.Endpoint{
				{
					DNSName:    n.config.DNSZone,
					RecordTTL:  ttl,
					RecordType: "NS",
					Targets:    NSServerList,
				},
				{
					DNSName:    n.nsServerName(),
					RecordTTL:  ttl,
					RecordType: "A",
					Targets:    NSServerIPs,
				},
			},
		},
	}
	res, err := n.ensureDNSEndpoint(k8gbNamespace, NSRecord)
	if err != nil {
		return res, err
	}
	return nil, nil
}

func (n *Route53) Finalize() (err error) {
	log.Info("Removing Zone Delegation entries...")
	dnsEndpointRoute53 := &externaldns.DNSEndpoint{}
	err = n.client.Get(context.Background(), client.ObjectKey{Namespace: k8gbNamespace, Name: "k8gb-ns-route53"}, dnsEndpointRoute53)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info(fmt.Sprint(err))
			return nil
		}
		return err
	}
	err = n.client.Delete(context.Background(), dnsEndpointRoute53)
	if err != nil {
		return err
	}
	return err
}

// TODO: reuse
func (n *Route53) nsServerName() string {
	dnsZoneIntoNS := strings.ReplaceAll(n.config.DNSZone, ".", "-")
	return fmt.Sprintf("gslb-ns-%s-%s.%s",
		dnsZoneIntoNS,
		n.config.ClusterGeoTag,
		n.config.EdgeDNSZone)
}

func (n *Route53) coreDNSExposedIPs() ([]string, error) {
	coreDNSService := &corev1.Service{}

	err := n.client.Get(context.TODO(), types.NamespacedName{Namespace: k8gbNamespace, Name: coreDNSExtServiceName}, coreDNSService)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info(fmt.Sprintf("Can't find %s service", coreDNSExtServiceName))
		}
		return nil, err
	}
	var lbHostname string
	if len(coreDNSService.Status.LoadBalancer.Ingress) > 0 {
		lbHostname = coreDNSService.Status.LoadBalancer.Ingress[0].Hostname
	} else {
		errMessage := fmt.Sprintf("no Ingress LoadBalancer entries found for %s serice", coreDNSExtServiceName)
		log.Info(errMessage)
		err := coreerrors.New(errMessage)
		return nil, err
	}
	IPs, err := utils.Dig(n.config.EdgeDNSServer, lbHostname)
	if err != nil {
		log.Info(fmt.Sprintf("Can't dig k8gb-coredns-lb service loadbalancer fqdn %s", lbHostname))
		return nil, err
	}
	return IPs, nil
}

func (n *Route53) ensureDNSEndpoint(namespace string, i *externaldns.DNSEndpoint) (*reconcile.Result, error) {
	found := &externaldns.DNSEndpoint{}
	err := n.client.Get(context.TODO(), types.NamespacedName{
		Name:      i.Name,
		Namespace: namespace,
	}, found)
	if err != nil && errors.IsNotFound(err) {

		// Create the DNSEndpoint
		log.Info(fmt.Sprintf("Creating a new DNSEndpoint:\n %s", prettyPrint(i)))
		err = n.client.Create(context.TODO(), i)

		if err != nil {
			// Creation failed
			log.Error(err, "Failed to create new DNSEndpoint", "DNSEndpoint.Namespace", i.Namespace, "DNSEndpoint.Name", i.Name)
			return &reconcile.Result{}, err
		}
		// Creation was successful
		return nil, nil
	} else if err != nil {
		// Error that isn't due to the service not existing
		log.Error(err, "Failed to get DNSEndpoint")
		return &reconcile.Result{}, err
	}

	// Update existing object with new spec
	found.Spec = i.Spec
	err = n.client.Update(context.TODO(), found)

	if err != nil {
		// Update failed
		log.Error(err, "Failed to update DNSEndpoint", "DNSEndpoint.Namespace", found.Namespace, "DNSEndpoint.Name", found.Name)
		return &reconcile.Result{}, err
	}
	return nil, nil
}

func prettyPrint(s interface{}) string {
	prettyStruct, err := json.MarshalIndent(s, "", "\t")
	if err != nil {
		fmt.Println("can't convert struct to json")
	}
	return string(prettyStruct)
}
