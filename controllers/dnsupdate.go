package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AbsaOSS/k8gb/controllers/internal/utils"

	"github.com/AbsaOSS/k8gb/controllers/depresolver"

	coreerrors "errors"

	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	ibclient "github.com/infobloxopen/infoblox-go-client"
	"github.com/miekg/dns"
	corev1 "k8s.io/api/core/v1"
	v1beta1 "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	externaldns "sigs.k8s.io/external-dns/endpoint"
)

const coreDNSExtServiceName = "k8gb-coredns-lb"

func (r *GslbReconciler) getGslbIngressIPs(gslb *k8gbv1beta1.Gslb) ([]string, error) {
	nn := types.NamespacedName{
		Name:      gslb.Name,
		Namespace: gslb.Namespace,
	}

	gslbIngress := &v1beta1.Ingress{}

	err := r.Get(context.TODO(), nn, gslbIngress)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info(fmt.Sprintf("Can't find gslb Ingress: %s", gslb.Name))
		}
		return nil, err
	}

	var gslbIngressIPs []string

	for _, ip := range gslbIngress.Status.LoadBalancer.Ingress {
		if len(ip.IP) > 0 {
			gslbIngressIPs = append(gslbIngressIPs, ip.IP)
		}
		if len(ip.Hostname) > 0 {
			IPs, err := utils.Dig(r.Config.EdgeDNSServer, ip.Hostname)
			if err != nil {
				log.Info("Dig error: %s", err)
				return nil, err
			}
			gslbIngressIPs = append(gslbIngressIPs, IPs...)
		}
	}

	return gslbIngressIPs, nil
}

func getExternalClusterHeartbeatFQDNs(gslb *k8gbv1beta1.Gslb, config *depresolver.Config) (extGslbClusters []string) {
	for _, geoTag := range config.ExtClustersGeoTags {
		extGslbClusters = append(extGslbClusters, fmt.Sprintf("%s-heartbeat-%s.%s", gslb.Name, geoTag, config.EdgeDNSZone))
	}
	return
}

func (r *GslbReconciler) getExternalTargets(host string) ([]string, error) {

	extGslbClusters := r.nsServerNameExt()

	var targets []string

	for _, cluster := range extGslbClusters {
		log.Info(fmt.Sprintf("Adding external Gslb targets from %s cluster...", cluster))
		g := new(dns.Msg)
		host = fmt.Sprintf("localtargets-%s.", host) // Convert to true FQDN with dot at the end. Otherwise dns lib freaks out
		g.SetQuestion(host, dns.TypeA)

		ns := overrideWithFakeDNS(r.Config.Override.FakeDNSEnabled, cluster)

		a, err := dns.Exchange(g, ns)
		if err != nil {
			log.Info(fmt.Sprintf("Error contacting external Gslb cluster(%s) : (%v)", cluster, err))
			return nil, nil
		}
		var clusterTargets []string

		for _, A := range a.Answer {
			IP := strings.Split(A.String(), "\t")[4]
			clusterTargets = append(clusterTargets, IP)
		}
		if len(clusterTargets) > 0 {
			targets = append(targets, clusterTargets...)
			log.Info(fmt.Sprintf("Added external %s Gslb targets from %s cluster", clusterTargets, cluster))
		}
	}

	return targets, nil
}

func (r *GslbReconciler) gslbDNSEndpoint(gslb *k8gbv1beta1.Gslb) (*externaldns.DNSEndpoint, error) {
	var gslbHosts []*externaldns.Endpoint
	var ttl = externaldns.TTL(gslb.Spec.Strategy.DNSTtlSeconds)

	serviceHealth, err := r.getServiceHealthStatus(gslb)
	if err != nil {
		return nil, err
	}

	localTargets, err := r.getGslbIngressIPs(gslb)
	if err != nil {
		return nil, err
	}

	for host, health := range serviceHealth {
		var finalTargets []string

		if !strings.Contains(host, r.Config.EdgeDNSZone) {
			return nil, fmt.Errorf("ingress host %s does not match delegated zone %s", host, r.Config.EdgeDNSZone)
		}

		if health == "Healthy" {
			finalTargets = append(finalTargets, localTargets...)
			localTargetsHost := fmt.Sprintf("localtargets-%s", host)
			dnsRecord := &externaldns.Endpoint{
				DNSName:    localTargetsHost,
				RecordTTL:  ttl,
				RecordType: "A",
				Targets:    localTargets,
			}
			gslbHosts = append(gslbHosts, dnsRecord)
		}

		// Check if host is alive on external Gslb
		externalTargets, err := r.getExternalTargets(host)
		if err != nil {
			return nil, err
		}
		if len(externalTargets) > 0 {
			switch gslb.Spec.Strategy.Type {
			case roundRobinStrategy:
				finalTargets = append(finalTargets, externalTargets...)
			case failoverStrategy:
				// If cluster is Primary
				if gslb.Spec.Strategy.PrimaryGeoTag == r.Config.ClusterGeoTag {
					// If cluster is Primary and Healthy return only own targets
					// If cluster is Primary and Unhealthy return Secondary external targets
					if health != "Healthy" {
						finalTargets = externalTargets
						log.Info(fmt.Sprintf("Executing failover strategy for %s Gslb on Primary. Workload on primary %s cluster is unhealthy, targets are %v",
							gslb.Name, gslb.Spec.Strategy.PrimaryGeoTag, finalTargets))
					}
				} else {
					// If cluster is Secondary and Primary external cluster is Healthy
					// then return Primary external targets.
					// Return own targets by default.
					finalTargets = externalTargets
					log.Info(fmt.Sprintf("Executing failover strategy for %s Gslb on Secondary. Workload on primary %s cluster is healthy, targets are %v",
						gslb.Name, gslb.Spec.Strategy.PrimaryGeoTag, finalTargets))
				}
			}
		} else {
			log.Info(fmt.Sprintf("No external targets have been found for host %s", host))
		}

		log.Info(fmt.Sprintf("Final target list for %s Gslb: %v", gslb.Name, finalTargets))

		if len(finalTargets) > 0 {
			dnsRecord := &externaldns.Endpoint{
				DNSName:    host,
				RecordTTL:  ttl,
				RecordType: "A",
				Targets:    finalTargets,
			}
			gslbHosts = append(gslbHosts, dnsRecord)
		}
	}
	dnsEndpointSpec := externaldns.DNSEndpointSpec{
		Endpoints: gslbHosts,
	}

	dnsEndpoint := &externaldns.DNSEndpoint{
		ObjectMeta: metav1.ObjectMeta{
			Name:        gslb.Name,
			Namespace:   gslb.Namespace,
			Annotations: map[string]string{"k8gb.absa.oss/dnstype": "local"},
		},
		Spec: dnsEndpointSpec,
	}

	err = controllerutil.SetControllerReference(gslb, dnsEndpoint, r.Scheme)
	if err != nil {
		return nil, err
	}
	return dnsEndpoint, err
}

func (r *GslbReconciler) nsServerName() string {
	dnsZoneIntoNS := strings.ReplaceAll(r.Config.DNSZone, ".", "-")
	return fmt.Sprintf("gslb-ns-%s-%s.%s",
		dnsZoneIntoNS,
		r.Config.ClusterGeoTag,
		r.Config.EdgeDNSZone)
}

func (r *GslbReconciler) nsServerNameExt() []string {

	dnsZoneIntoNS := strings.ReplaceAll(r.Config.DNSZone, ".", "-")
	var extNSServers []string
	for _, clusterGeoTag := range r.Config.ExtClustersGeoTags {
		extNSServers = append(extNSServers,
			fmt.Sprintf("gslb-ns-%s-%s.%s",
				dnsZoneIntoNS,
				clusterGeoTag,
				r.Config.EdgeDNSZone))
	}

	return extNSServers
}

type fakeInfobloxConnector struct {
	// createObjectObj interface{}

	getObjectObj interface{}
	getObjectRef string

	// deleteObjectRef string

	// updateObjectObj interface{}
	// updateObjectRef string

	resultObject interface{}

	fakeRefReturn string
}

func (c *fakeInfobloxConnector) CreateObject(ibclient.IBObject) (string, error) {
	return c.fakeRefReturn, nil
}

func (c *fakeInfobloxConnector) GetObject(ibclient.IBObject, string, interface{}) (err error) {
	return nil
}

func (c *fakeInfobloxConnector) DeleteObject(string) (string, error) {
	return c.fakeRefReturn, nil
}

func (c *fakeInfobloxConnector) UpdateObject(ibclient.IBObject, string) (string, error) {
	return c.fakeRefReturn, nil
}

func infobloxConnection(config *depresolver.Config) (*ibclient.ObjectManager, error) {
	hostConfig := ibclient.HostConfig{
		Host:     config.Infoblox.Host,
		Version:  config.Infoblox.Version,
		Port:     strconv.Itoa(config.Infoblox.Port),
		Username: config.Infoblox.Username,
		Password: config.Infoblox.Password,
	}
	transportConfig := ibclient.NewTransportConfig("false", 20, 10)
	requestBuilder := &ibclient.WapiRequestBuilder{}
	requestor := &ibclient.WapiHttpRequestor{}

	var objMgr *ibclient.ObjectManager

	if config.Override.FakeInfobloxEnabled {
		fqdn := "fakezone.example.com"
		fakeRefReturn := "zone_delegated/ZG5zLnpvbmUkLl9kZWZhdWx0LnphLmNvLmFic2EuY2Fhcy5vaG15Z2xiLmdzbGJpYmNsaWVudA:fakezone.example.com/default"
		ohmyFakeConnector := &fakeInfobloxConnector{
			getObjectObj: ibclient.NewZoneDelegated(ibclient.ZoneDelegated{Fqdn: fqdn}),
			getObjectRef: "",
			resultObject: []ibclient.ZoneDelegated{*ibclient.NewZoneDelegated(ibclient.ZoneDelegated{Fqdn: fqdn, Ref: fakeRefReturn})},
		}
		objMgr = ibclient.NewObjectManager(ohmyFakeConnector, "ohmyclient", "")
	} else {
		conn, err := ibclient.NewConnector(hostConfig, transportConfig, requestBuilder, requestor)
		if err != nil {
			return nil, err
		}
		defer func() {
			err = conn.Logout()
			if err != nil {
				log.Error(err, "Failed to close connection to infoblox")
			}
		}()
		objMgr = ibclient.NewObjectManager(conn, "ohmyclient", "")
	}
	return objMgr, nil
}

func checkAliveFromTXT(fqdn string, config *depresolver.Config, splitBrainThreshold time.Duration) error {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(fqdn), dns.TypeTXT)
	ns := overrideWithFakeDNS(config.Override.FakeDNSEnabled, config.EdgeDNSServer)
	txt, err := dns.Exchange(m, ns)
	if err != nil {
		log.Info(fmt.Sprintf("Error contacting EdgeDNS server (%s) for TXT split brain record: (%s)", ns, err))
		return err
	}
	var timestamp string
	if len(txt.Answer) > 0 {
		if t, ok := txt.Answer[0].(*dns.TXT); ok {
			log.Info(fmt.Sprintf("Split brain TXT raw record: %s", t.String()))
			timestamp = strings.Split(t.String(), "\t")[4]
			timestamp = strings.Trim(timestamp, "\"") // Otherwise time.Parse() will miserably fail
		}
	}

	if len(timestamp) > 0 {
		log.Info(fmt.Sprintf("Split brain TXT raw time stamp: %s", timestamp))
		timeFromTXT, err := time.Parse("2006-01-02T15:04:05", timestamp)
		if err != nil {
			return err
		}

		log.Info(fmt.Sprintf("Split brain TXT parsed time stamp: %s", timeFromTXT))
		now := time.Now().UTC()

		diff := now.Sub(timeFromTXT)
		log.Info(fmt.Sprintf("Split brain TXT time diff: %s", diff))

		if diff > splitBrainThreshold {
			return errors.NewGone(fmt.Sprintf("Split brain TXT record expired the time threshold: (%s)", splitBrainThreshold))
		}

		return nil
	}
	return errors.NewGone(fmt.Sprintf("Can't find split brain TXT record at EdgeDNS server(%s) and record %s ", ns, fqdn))

}

func filterOutDelegateTo(delegateTo []ibclient.NameServer, fqdn string) []ibclient.NameServer {
	for i := 0; i < len(delegateTo); i++ {
		if delegateTo[i].Name == fqdn {
			delegateTo = append(delegateTo[:i], delegateTo[i+1:]...)
			i--
		}
	}
	return delegateTo
}

func (r *GslbReconciler) coreDNSExposedIPs() ([]string, error) {
	coreDNSService := &corev1.Service{}

	err := r.Get(context.TODO(), types.NamespacedName{Namespace: r.Config.K8gbNamespace, Name: coreDNSExtServiceName}, coreDNSService)
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
	IPs, err := utils.Dig(r.Config.EdgeDNSServer, lbHostname)
	if err != nil {
		log.Info(fmt.Sprintf("Can't dig k8gb-coredns-lb service loadbalancer fqdn %s (%s)", lbHostname, err))
		return nil, err
	}
	return IPs, nil
}

func (r *GslbReconciler) configureZoneDelegation(gslb *k8gbv1beta1.Gslb) (*reconcile.Result, error) {
	switch r.Config.EdgeDNSType {
	case depresolver.DNSTypeRoute53:
		return r.Route53.CreateZoneDelegationForExternalDNS(gslb)
	case depresolver.DNSTypeNS1:
		return r.NS1.CreateZoneDelegationForExternalDNS(gslb)
	case depresolver.DNSTypeInfoblox:
		objMgr, err := infobloxConnection(r.Config)
		if err != nil {
			return &reconcile.Result{}, err
		}
		addresses, err := r.getGslbIngressIPs(gslb)
		if err != nil {
			return &reconcile.Result{}, err
		}
		var delegateTo []ibclient.NameServer

		for _, address := range addresses {
			nameServer := ibclient.NameServer{Address: address, Name: r.nsServerName()}
			delegateTo = append(delegateTo, nameServer)
		}

		findZone, err := objMgr.GetZoneDelegated(r.Config.DNSZone)
		if err != nil {
			return &reconcile.Result{}, err
		}

		if findZone != nil {
			err = checkZoneDelegated(findZone, r.Config.DNSZone)
			if err != nil {
				return &reconcile.Result{}, err
			}
			if len(findZone.Ref) > 0 {

				// Drop own records for straight away update
				existingDelegateTo := filterOutDelegateTo(findZone.DelegateTo, r.nsServerName())
				existingDelegateTo = append(existingDelegateTo, delegateTo...)

				// Drop external records if they are stale
				extClusters := getExternalClusterHeartbeatFQDNs(gslb, r.Config)
				for _, extCluster := range extClusters {
					err = checkAliveFromTXT(extCluster, r.Config, time.Second*time.Duration(gslb.Spec.Strategy.SplitBrainThresholdSeconds))
					if err != nil {
						log.Error(err, "got the error from TXT based checkAlive")
						log.Info(fmt.Sprintf("External cluster (%s) doesn't look alive, filtering it out from delegated zone configuration...",
							extCluster))
						existingDelegateTo = filterOutDelegateTo(existingDelegateTo, extCluster)
					}
				}
				log.Info(fmt.Sprintf("Updating delegated zone(%s) with the server list(%v)", r.Config.DNSZone, existingDelegateTo))

				_, err = objMgr.UpdateZoneDelegated(findZone.Ref, existingDelegateTo)
				if err != nil {
					return &reconcile.Result{}, err
				}
			}
		} else {
			log.Info(fmt.Sprintf("Creating delegated zone(%s)...", r.Config.DNSZone))
			_, err = objMgr.CreateZoneDelegated(r.Config.DNSZone, delegateTo)
			if err != nil {
				return &reconcile.Result{}, err
			}
		}

		edgeTimestamp := fmt.Sprint(time.Now().UTC().Format("2006-01-02T15:04:05"))
		heartbeatTXTName := fmt.Sprintf("%s-heartbeat-%s.%s", gslb.Name, r.Config.ClusterGeoTag, r.Config.EdgeDNSZone)
		heartbeatTXTRecord, err := objMgr.GetTXTRecord(heartbeatTXTName)
		if err != nil {
			return &reconcile.Result{}, err
		}
		if heartbeatTXTRecord == nil {
			log.Info(fmt.Sprintf("Creating split brain TXT record(%s)...", heartbeatTXTName))
			_, err := objMgr.CreateTXTRecord(heartbeatTXTName, edgeTimestamp, gslb.Spec.Strategy.DNSTtlSeconds, "default")
			if err != nil {
				return &reconcile.Result{}, err
			}
		} else {
			log.Info(fmt.Sprintf("Updating split brain TXT record(%s)...", heartbeatTXTName))
			_, err := objMgr.UpdateTXTRecord(heartbeatTXTName, edgeTimestamp)
			if err != nil {
				return &reconcile.Result{}, err
			}
		}
	case depresolver.DNSTypeNoEdgeDNS:
		return nil, nil
	}
	return nil, coreerrors.New("unhandled DNS type...")
}

func (r *GslbReconciler) ensureDNSEndpoint(
	namespace string,
	i *externaldns.DNSEndpoint,
) (*reconcile.Result, error) {
	found := &externaldns.DNSEndpoint{}
	err := r.Get(context.TODO(), types.NamespacedName{
		Name:      i.Name,
		Namespace: namespace,
	}, found)
	if err != nil && errors.IsNotFound(err) {

		// Create the DNSEndpoint
		log.Info(fmt.Sprintf("Creating a new DNSEndpoint:\n %s", utils.ToString(i)))
		err = r.Create(context.TODO(), i)

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
	err = r.Update(context.TODO(), found)

	if err != nil {
		// Update failed
		log.Error(err, "Failed to update DNSEndpoint", "DNSEndpoint.Namespace", found.Namespace, "DNSEndpoint.Name", found.Name)
		return &reconcile.Result{}, err
	}

	return nil, nil
}

func checkZoneDelegated(findZone *ibclient.ZoneDelegated, gslbZoneName string) error {
	if findZone.Fqdn != gslbZoneName {
		err := fmt.Errorf("delegated zone returned from infoblox(%s) does not match requested gslb zone(%s)", findZone.Fqdn, gslbZoneName)
		return err
	}
	return nil
}

func overrideWithFakeDNS(fakeDNSEnabled bool, server string) (ns string) {
	if fakeDNSEnabled {
		ns = "127.0.0.1:7753"
	} else {
		ns = fmt.Sprintf("%s:53", server)
	}
	return
}
