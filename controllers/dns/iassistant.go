package dns

import (
	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	externaldns "sigs.k8s.io/external-dns/endpoint"
)

type IAssistant interface {
	// CoreDNSExposedIPs retrieves list of exposed IP by CoreDNS
	CoreDNSExposedIPs() ([]string, error)
	// GslbIngressExposedIPs retrieves list of IP's exposed by all GSLB ingresses
	GslbIngressExposedIPs(gslb *k8gbv1beta1.Gslb) ([]string, error)
	// SaveDNSEndpoint update DNS endpoint or create new one if doesnt exist
	SaveDNSEndpoint(i *externaldns.DNSEndpoint) (*reconcile.Result, error)
	// RemoveEndpoint removes endpoint
	RemoveEndpoint(gslb *k8gbv1beta1.Gslb, endpointName string) error
}
