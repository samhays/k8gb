package dns

import (
	"fmt"
	"sort"

	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	"github.com/AbsaOSS/k8gb/controllers/depresolver"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	externaldns "sigs.k8s.io/external-dns/endpoint"
)

type ExternalDNSType string

const (
	ExternalDNSTypeNS1     ExternalDNSType = "ns1"
	ExternalDnsTypeRoute53 ExternalDNSType = " route53"
)

type ExternalDNS struct {
	assistant    IAssistant
	dnsType      ExternalDNSType
	config       depresolver.Config
	endpointName string
}

func NewExternalDNS(dnsType ExternalDNSType, config depresolver.Config, assistant IAssistant) *ExternalDNS {
	return &ExternalDNS{
		assistant:    assistant,
		dnsType:      dnsType,
		config:       config,
		endpointName: fmt.Sprintf("k8gb-ns-%s", dnsType),
	}
}

func (e *ExternalDNS) HandleZoneDelegation(gslb *k8gbv1beta1.Gslb) (*reconcile.Result, error) {
	ttl := externaldns.TTL(gslb.Spec.Strategy.DNSTtlSeconds)
	// TODO: extract log.Info(fmt.Sprintf("Creating/Updating DNSEndpoint CRDs for %s...", e.dnsType))
	var NSServerList []string
	NSServerList = append(NSServerList, nsServerName(e.config))
	NSServerList = append(NSServerList, nsServerNameExt(e.config)...)
	sort.Strings(NSServerList)
	var NSServerIPs []string
	var err error
	if e.config.CoreDNSExposed {
		NSServerIPs, err = e.assistant.CoreDNSExposedIPs()
	} else {
		NSServerIPs, err = e.assistant.GslbIngressExposedIPs(gslb)
	}
	if err != nil {
		return &reconcile.Result{}, err
	}
	NSRecord := &externaldns.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:        e.endpointName,
			Namespace:   e.config.K8gbNamespace,
			Annotations: map[string]string{"k8gb.absa.oss/dnstype": string(e.dnsType)},
		},
		Spec: externaldns.DNSEndpointSpec{
			Endpoints: []*externaldns.Endpoint{
				{
					DNSName:    e.config.DNSZone,
					RecordTTL:  ttl,
					RecordType: "NS",
					Targets:    NSServerList,
				},
				{
					DNSName:    nsServerName(e.config),
					RecordTTL:  ttl,
					RecordType: "A",
					Targets:    NSServerIPs,
				},
			},
		},
	}
	res, err := e.assistant.SaveDNSEndpoint(NSRecord)
	if err != nil {
		return res, err
	}
	return nil, nil
}

func (e *ExternalDNS) Finalize(gslb *k8gbv1beta1.Gslb) error {
	return e.assistant.RemoveEndpoint(gslb, e.endpointName)
}
