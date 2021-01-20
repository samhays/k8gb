package dns

import (
	"context"
	coreerrors "errors"
	"fmt"

	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	externaldns "sigs.k8s.io/external-dns/endpoint"

	"github.com/AbsaOSS/k8gb/controllers/internal/utils"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	v1beta1 "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const coreDNSExtServiceName = "k8gb-coredns-lb"

// GslbAssistant is common wrapper operating on GSLB instance
// it directly logs messages into logr.Logger and use apimachinery client
// to call kubernetes API
type GslbAssistant struct {
	log           logr.Logger
	client        client.Client
	k8gbNamespace string
	edgeDNSServer string
}

func NewGslbAssistant(client client.Client, log logr.Logger, k8gbNamespace, edgeDNSServer string) *GslbAssistant {
	return &GslbAssistant{
		client:        client,
		log:           log,
		k8gbNamespace: k8gbNamespace,
		edgeDNSServer: edgeDNSServer,
	}
}

// TODO: consider moving *gslb parameters to constructor
// CoreDNSExposedIPs retrieves list of IP's exposed by CoreDNS
func (r *GslbAssistant) CoreDNSExposedIPs() ([]string, error) {
	coreDNSService := &corev1.Service{}
	err := r.client.Get(context.TODO(),
		types.NamespacedName{Namespace: r.k8gbNamespace, Name: coreDNSExtServiceName}, coreDNSService)
	if err != nil {
		if errors.IsNotFound(err) {
			r.log.Info(fmt.Sprintf("Can't find %s service", coreDNSExtServiceName))
		}
		return nil, err
	}
	var lbHostname string
	if len(coreDNSService.Status.LoadBalancer.Ingress) > 0 {
		lbHostname = coreDNSService.Status.LoadBalancer.Ingress[0].Hostname
	} else {
		errMessage := fmt.Sprintf("no Ingress LoadBalancer entries found for %s serice", coreDNSExtServiceName)
		r.log.Info(errMessage)
		err := coreerrors.New(errMessage)
		return nil, err
	}
	IPs, err := utils.Dig(r.edgeDNSServer, lbHostname)
	if err != nil {
		r.log.Info(fmt.Sprintf("Can't dig k8gb-coredns-lb service loadbalancer fqdn %s (%s)", lbHostname, err))
		return nil, err
	}
	return IPs, nil
}

// GslbIngressExposedIPs retrieves list of IP's exposed by all GSLB ingresses
func (r *GslbAssistant) GslbIngressExposedIPs(gslb *k8gbv1beta1.Gslb) ([]string, error) {
	nn := types.NamespacedName{
		Name:      gslb.Name,
		Namespace: gslb.Namespace,
	}

	gslbIngress := &v1beta1.Ingress{}

	err := r.client.Get(context.TODO(), nn, gslbIngress)
	if err != nil {
		if errors.IsNotFound(err) {
			r.log.Info(fmt.Sprintf("Can't find gslb Ingress: %s", gslb.Name))
		}
		return nil, err
	}

	var gslbIngressIPs []string

	for _, ip := range gslbIngress.Status.LoadBalancer.Ingress {
		if len(ip.IP) > 0 {
			gslbIngressIPs = append(gslbIngressIPs, ip.IP)
		}
		if len(ip.Hostname) > 0 {
			IPs, err := utils.Dig(r.edgeDNSServer, ip.Hostname)
			if err != nil {
				r.log.Info("Dig error: %s", err)
				return nil, err
			}
			gslbIngressIPs = append(gslbIngressIPs, IPs...)
		}
	}

	return gslbIngressIPs, nil
}

// SaveDNSEndpoint update DNS endpoint or create new one if doesnt exist
func (r *GslbAssistant) SaveDNSEndpoint(i *externaldns.DNSEndpoint) (*reconcile.Result, error) {
	found := &externaldns.DNSEndpoint{}
	err := r.client.Get(context.TODO(), types.NamespacedName{
		Name:      i.Name,
		Namespace: r.k8gbNamespace,
	}, found)
	if err != nil && errors.IsNotFound(err) {

		// Create the DNSEndpoint
		r.log.Info(fmt.Sprintf("Creating a new DNSEndpoint:\n %s", utils.ToString(i)))
		err = r.client.Create(context.TODO(), i)

		if err != nil {
			// Creation failed
			r.log.Error(err, "Failed to create new DNSEndpoint",
				"DNSEndpoint.Namespace", i.Namespace, "DNSEndpoint.Name", i.Name)
			return &reconcile.Result{}, err
		}
		// Creation was successful
		return nil, nil
	} else if err != nil {
		// Error that isn't due to the service not existing
		r.log.Error(err, "Failed to get DNSEndpoint")
		return &reconcile.Result{}, err
	}

	// Update existing object with new spec
	found.Spec = i.Spec
	err = r.client.Update(context.TODO(), found)

	if err != nil {
		// Update failed
		r.log.Error(err, "Failed to update DNSEndpoint",
			"DNSEndpoint.Namespace", found.Namespace, "DNSEndpoint.Name", found.Name)
		return &reconcile.Result{}, err
	}
	return nil, nil
}

// RemoveEndpoint removes endpoint
func (r *GslbAssistant) RemoveEndpoint(gslb *k8gbv1beta1.Gslb, endpointName string) error {
	// TODO: expose log message	r.log.Info("Removing Zone Delegation entries...")
	r.log.Info("Removing endpoint %s.%s", r.k8gbNamespace, endpointName)
	dnsEndpoint := &externaldns.DNSEndpoint{}
	err := r.client.Get(context.Background(), client.ObjectKey{Namespace: r.k8gbNamespace, Name: endpointName}, dnsEndpoint)
	if err != nil {
		if errors.IsNotFound(err) {
			r.log.Info(fmt.Sprint(err))
			return nil
		}
		return err
	}
	err = r.client.Delete(context.TODO(), dnsEndpoint)
	return err
}
