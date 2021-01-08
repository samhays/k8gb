package infoblox

import (
	"context"
	coreerrors "errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AbsaOSS/k8gb/controllers/depresolver"
	"github.com/AbsaOSS/k8gb/controllers/internal/utils"
	ibclient "github.com/infobloxopen/infoblox-go-client"
	"github.com/miekg/dns"
	"k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var log = logf.Log.WithName("controller_gslb")

type Infoblox struct {
	gslb   *k8gbv1beta1.Gslb
	config *depresolver.Config
	client client.Client
}

func NewInfoblox(config *depresolver.Config, gslb *k8gbv1beta1.Gslb, client client.Client) (i *Infoblox, err error) {
	if gslb == nil {
		return i, fmt.Errorf("nil *Gslb")
	}
	if config == nil {
		return i, fmt.Errorf("nil *Config")
	}
	i = &Infoblox{
		gslb:   gslb,
		config: config,
		client: client,
	}
	return
}

func (i *Infoblox) ConfigureZoneDelegation() (r *reconcile.Result, err error) {
	objMgr, err := i.infobloxConnection()
	if err != nil {
		return &reconcile.Result{}, err
	}
	addresses, err := i.getGslbIngressIPs(i.gslb)
	if err != nil {
		return &reconcile.Result{}, err
	}
	var delegateTo []ibclient.NameServer

	for _, address := range addresses {
		nameServer := ibclient.NameServer{Address: address, Name: i.nsServerName()}
		delegateTo = append(delegateTo, nameServer)
	}

	findZone, err := objMgr.GetZoneDelegated(i.config.DNSZone)
	if err != nil {
		return &reconcile.Result{}, err
	}

	if findZone != nil {
		err = i.checkZoneDelegated(findZone, i.config.DNSZone)
		if err != nil {
			return &reconcile.Result{}, err
		}
		if len(findZone.Ref) > 0 {

			// Drop own records for straight away update
			existingDelegateTo := i.filterOutDelegateTo(findZone.DelegateTo, i.nsServerName())
			existingDelegateTo = append(existingDelegateTo, delegateTo...)

			// Drop external records if they are stale
			extClusters := i.getExternalClusterHeartbeatFQDNs()
			for _, extCluster := range extClusters {
				err = i.checkAliveFromTXT(extCluster, time.Second*time.Duration(i.gslb.Spec.Strategy.SplitBrainThresholdSeconds))
				if err != nil {
					log.Error(err, "got the error from TXT based checkAlive")
					log.Info(fmt.Sprintf("External cluster (%s) doesn't look alive, filtering it out from delegated zone configuration...", extCluster))
					existingDelegateTo = i.filterOutDelegateTo(existingDelegateTo, extCluster)
				}
			}
			log.Info(fmt.Sprintf("Updating delegated zone(%s) with the server list(%v)", i.config.DNSZone, existingDelegateTo))

			_, err = objMgr.UpdateZoneDelegated(findZone.Ref, existingDelegateTo)
			if err != nil {
				return &reconcile.Result{}, err
			}
		}
	} else {
		log.Info(fmt.Sprintf("Creating delegated zone(%s)...", i.config.DNSZone))
		_, err = objMgr.CreateZoneDelegated(i.config.DNSZone, delegateTo)
		if err != nil {
			return &reconcile.Result{}, err
		}
	}

	edgeTimestamp := fmt.Sprint(time.Now().UTC().Format("2006-01-02T15:04:05"))
	heartbeatTXTName := fmt.Sprintf("%s-heartbeat-%s.%s", i.gslb.Name, i.config.ClusterGeoTag, i.config.EdgeDNSZone)
	heartbeatTXTRecord, err := objMgr.GetTXTRecord(heartbeatTXTName)
	if err != nil {
		return &reconcile.Result{}, err
	}
	if heartbeatTXTRecord == nil {
		log.Info(fmt.Sprintf("Creating split brain TXT record(%s)...", heartbeatTXTName))
		_, err := objMgr.CreateTXTRecord(heartbeatTXTName, edgeTimestamp, i.gslb.Spec.Strategy.DNSTtlSeconds, "default")
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
	return nil, coreerrors.New("unhandled DNS type")
}

func (i *Infoblox) Finalize() (err error) {
	objMgr, err := i.infobloxConnection()
	if err != nil {
		return err
	}
	findZone, err := objMgr.GetZoneDelegated(i.config.DNSZone)
	if err != nil {
		return err
	}

	if findZone != nil {
		err = i.checkZoneDelegated(findZone, i.config.DNSZone)
		if err != nil {
			return err
		}
		if len(findZone.Ref) > 0 {
			log.Info(fmt.Sprintf("Deleting delegated zone(%s)...", i.config.DNSZone))
			_, err := objMgr.DeleteZoneDelegated(findZone.Ref)
			if err != nil {
				return err
			}
		}
	}

	heartbeatTXTName := fmt.Sprintf("%s-heartbeat-%s.%s", i.gslb.Name, i.config.ClusterGeoTag, i.config.EdgeDNSZone)
	findTXT, err := objMgr.GetTXTRecord(heartbeatTXTName)
	if err != nil {
		return err
	}

	if findTXT != nil {
		if len(findTXT.Ref) > 0 {
			log.Info(fmt.Sprintf("Deleting split brain TXT record(%s)...", heartbeatTXTName))
			_, err := objMgr.DeleteTXTRecord(findTXT.Ref)
			if err != nil {
				return err
			}
		}
	}
	return err
}

func (i *Infoblox) infobloxConnection() (*ibclient.ObjectManager, error) {
	hostConfig := ibclient.HostConfig{
		Host:     i.config.Infoblox.Host,
		Version:  i.config.Infoblox.Version,
		Port:     strconv.Itoa(i.config.Infoblox.Port),
		Username: i.config.Infoblox.Username,
		Password: i.config.Infoblox.Password,
	}
	transportConfig := ibclient.NewTransportConfig("false", 20, 10)
	requestBuilder := &ibclient.WapiRequestBuilder{}
	requestor := &ibclient.WapiHttpRequestor{}

	var objMgr *ibclient.ObjectManager

	if i.config.Override.FakeInfobloxEnabled {
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

func (i *Infoblox) getGslbIngressIPs(gslb *k8gbv1beta1.Gslb) ([]string, error) {
	nn := types.NamespacedName{
		Name:      gslb.Name,
		Namespace: gslb.Namespace,
	}

	gslbIngress := &v1beta1.Ingress{}

	err := i.client.Get(context.TODO(), nn, gslbIngress)
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
			IPs, err := utils.Dig(i.config.EdgeDNSServer, ip.Hostname)
			if err != nil {
				log.Info(err.Error())
				return nil, err
			}
			gslbIngressIPs = append(gslbIngressIPs, IPs...)
		}
	}

	return gslbIngressIPs, nil
}

func (i *Infoblox) nsServerName() string {
	dnsZoneIntoNS := strings.ReplaceAll(i.config.DNSZone, ".", "-")
	return fmt.Sprintf("gslb-ns-%s-%s.%s",
		dnsZoneIntoNS,
		i.config.ClusterGeoTag,
		i.config.EdgeDNSZone)
}

func (i *Infoblox) checkZoneDelegated(findZone *ibclient.ZoneDelegated, gslbZoneName string) error {
	if findZone.Fqdn != gslbZoneName {
		err := fmt.Errorf("delegated zone returned from infoblox(%s) does not match requested gslb zone(%s)", findZone.Fqdn, gslbZoneName)
		return err
	}
	return nil
}

func (i *Infoblox) filterOutDelegateTo(delegateTo []ibclient.NameServer, fqdn string) []ibclient.NameServer {
	for i := 0; i < len(delegateTo); i++ {
		if delegateTo[i].Name == fqdn {
			delegateTo = append(delegateTo[:i], delegateTo[i+1:]...)
			i--
		}
	}
	return delegateTo
}

func (i *Infoblox) getExternalClusterHeartbeatFQDNs() (extGslbClusters []string) {
	for _, geoTag := range i.config.ExtClustersGeoTags {
		extGslbClusters = append(extGslbClusters, fmt.Sprintf("%s-heartbeat-%s.%s", i.gslb.Name, geoTag, i.config.EdgeDNSZone))
	}
	return
}

func (i *Infoblox) checkAliveFromTXT(fqdn string, splitBrainThreshold time.Duration) error {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(fqdn), dns.TypeTXT)
	ns := overrideWithFakeDNS(i.config.Override.FakeDNSEnabled, i.config.EdgeDNSServer)
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

func overrideWithFakeDNS(fakeDNSEnabled bool, server string) (ns string) {
	if fakeDNSEnabled {
		ns = "127.0.0.1:7753"
	} else {
		ns = fmt.Sprintf("%s:53", server)
	}
	return
}
