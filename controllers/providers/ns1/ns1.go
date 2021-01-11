package ns1

import (
	"context"
	"fmt"
	"os"

	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	"github.com/AbsaOSS/k8gb/controllers/depresolver"
	"github.com/AbsaOSS/k8gb/controllers/internal/utils"
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
	providerName          = "ns1"
	coreDNSExtServiceName = "k8gb-coredns-lb"
)

var log = logf.Log.WithName("controller_gslb")

// TODO: move to depresolver
var k8gbNamespace = os.Getenv("POD_NAMESPACE")

type Ns1 struct {
	gslb   *k8gbv1beta1.Gslb
	config *depresolver.Config
	client client.Client
}

func NewNs1(config *depresolver.Config, gslb *k8gbv1beta1.Gslb, client client.Client) (i *Ns1, err error) {
	if gslb == nil {
		return i, fmt.Errorf("nil *Gslb")
	}
	if config == nil {
		return i, fmt.Errorf("nil *Config")
	}
	i = &Ns1{
		gslb:   gslb,
		config: config,
		client: client,
	}
	return
}

func (p *Ns1) ConfigureZoneDelegation() (r *reconcile.Result, err error) {
	ttl := externaldns.TTL(p.gslb.Spec.Strategy.DNSTtlSeconds)
	log.Info(fmt.Sprintf("Creating/Updating DNSEndpoint CRDs for %s...", providerName))
	var NSServerList []string
	NSServerList = append(NSServerList, p.nsServerName())
	NSServerList = append(NSServerList, utils.NsServerNameExt(p.config.DNSZone, p.config.EdgeDNSZone, p.config.ExtClustersGeoTags)...)
	sort.Strings(NSServerList)
	NSServerIPs, err := utils.CoreDNSExposedIPs(p.client, p.config.EdgeDNSServer, k8gbNamespace, coreDNSExtServiceName)
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
					DNSName:    p.config.DNSZone,
					RecordTTL:  ttl,
					RecordType: "NS",
					Targets:    NSServerList,
				},
				{
					DNSName:    p.nsServerName(),
					RecordTTL:  ttl,
					RecordType: "A",
					Targets:    NSServerIPs,
				},
			},
		},
	}
	res, err := p.ensureDNSEndpoint(k8gbNamespace, NSRecord)
	if err != nil {
		return res, err
	}
	return nil, nil
}

// TODO: missing finalizer code in original
func (p *Ns1) Finalize() (err error) {
	log.Info("Successfully finalized Gslb")
	return
}

// TODO: reuse
func (p *Ns1) nsServerName() string {
	dnsZoneIntoNS := strings.ReplaceAll(p.config.DNSZone, ".", "-")
	return fmt.Sprintf("gslb-ns-%s-%s.%s",
		dnsZoneIntoNS,
		p.config.ClusterGeoTag,
		p.config.EdgeDNSZone)
}

func (p *Ns1) ensureDNSEndpoint(namespace string, i *externaldns.DNSEndpoint) (*reconcile.Result, error) {
	found := &externaldns.DNSEndpoint{}
	err := p.client.Get(context.TODO(), types.NamespacedName{
		Name:      i.Name,
		Namespace: namespace,
	}, found)
	if err != nil && errors.IsNotFound(err) {

		// Create the DNSEndpoint
		log.Info(fmt.Sprintf("Creating a new DNSEndpoint:\n %s", utils.PrettyPrint(i)))
		err = p.client.Create(context.TODO(), i)

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
	err = p.client.Update(context.TODO(), found)

	if err != nil {
		// Update failed
		log.Error(err, "Failed to update DNSEndpoint", "DNSEndpoint.Namespace", found.Namespace, "DNSEndpoint.Name", found.Name)
		return &reconcile.Result{}, err
	}
	return nil, nil
}
