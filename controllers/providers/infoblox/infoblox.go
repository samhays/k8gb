package infoblox

import (
	coreerrors "errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AbsaOSS/k8gb/controllers/depresolver"
	"github.com/AbsaOSS/k8gb/controllers/internal/utils"
	ibclient "github.com/infobloxopen/infoblox-go-client"
	"github.com/miekg/dns"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
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

func (p *Infoblox) ConfigureZoneDelegation() (r *reconcile.Result, err error) {
	objMgr, err := p.infobloxConnection()
	if err != nil {
		return &reconcile.Result{}, err
	}
	addresses, err := utils.GetGslbIngressIPs(p.gslb, p.client, p.config.EdgeDNSServer)
	if err != nil {
		return &reconcile.Result{}, err
	}
	var delegateTo []ibclient.NameServer

	for _, address := range addresses {
		nameServer := ibclient.NameServer{Address: address, Name: p.nsServerName()}
		delegateTo = append(delegateTo, nameServer)
	}

	findZone, err := objMgr.GetZoneDelegated(p.config.DNSZone)
	if err != nil {
		return &reconcile.Result{}, err
	}

	if findZone != nil {
		err = p.checkZoneDelegated(findZone, p.config.DNSZone)
		if err != nil {
			return &reconcile.Result{}, err
		}
		if len(findZone.Ref) > 0 {

			// Drop own records for straight away update
			existingDelegateTo := p.filterOutDelegateTo(findZone.DelegateTo, p.nsServerName())
			existingDelegateTo = append(existingDelegateTo, delegateTo...)

			// Drop external records if they are stale
			extClusters := p.getExternalClusterHeartbeatFQDNs()
			for _, extCluster := range extClusters {
				err = p.checkAliveFromTXT(extCluster, time.Second*time.Duration(p.gslb.Spec.Strategy.SplitBrainThresholdSeconds))
				if err != nil {
					log.Error(err, "got the error from TXT based checkAlive")
					log.Info(fmt.Sprintf("External cluster (%s) doesn't look alive, filtering it out from delegated zone configuration...", extCluster))
					existingDelegateTo = p.filterOutDelegateTo(existingDelegateTo, extCluster)
				}
			}
			log.Info(fmt.Sprintf("Updating delegated zone(%s) with the server list(%v)", p.config.DNSZone, existingDelegateTo))

			_, err = objMgr.UpdateZoneDelegated(findZone.Ref, existingDelegateTo)
			if err != nil {
				return &reconcile.Result{}, err
			}
		}
	} else {
		log.Info(fmt.Sprintf("Creating delegated zone(%s)...", p.config.DNSZone))
		_, err = objMgr.CreateZoneDelegated(p.config.DNSZone, delegateTo)
		if err != nil {
			return &reconcile.Result{}, err
		}
	}

	edgeTimestamp := fmt.Sprint(time.Now().UTC().Format("2006-01-02T15:04:05"))
	heartbeatTXTName := fmt.Sprintf("%s-heartbeat-%s.%s", p.gslb.Name, p.config.ClusterGeoTag, p.config.EdgeDNSZone)
	heartbeatTXTRecord, err := objMgr.GetTXTRecord(heartbeatTXTName)
	if err != nil {
		return &reconcile.Result{}, err
	}
	if heartbeatTXTRecord == nil {
		log.Info(fmt.Sprintf("Creating split brain TXT record(%s)...", heartbeatTXTName))
		_, err := objMgr.CreateTXTRecord(heartbeatTXTName, edgeTimestamp, p.gslb.Spec.Strategy.DNSTtlSeconds, "default")
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

func (p *Infoblox) Finalize() (err error) {
	objMgr, err := p.infobloxConnection()
	if err != nil {
		return err
	}
	findZone, err := objMgr.GetZoneDelegated(p.config.DNSZone)
	if err != nil {
		return err
	}

	if findZone != nil {
		err = p.checkZoneDelegated(findZone, p.config.DNSZone)
		if err != nil {
			return err
		}
		if len(findZone.Ref) > 0 {
			log.Info(fmt.Sprintf("Deleting delegated zone(%s)...", p.config.DNSZone))
			_, err := objMgr.DeleteZoneDelegated(findZone.Ref)
			if err != nil {
				return err
			}
		}
	}

	heartbeatTXTName := fmt.Sprintf("%s-heartbeat-%s.%s", p.gslb.Name, p.config.ClusterGeoTag, p.config.EdgeDNSZone)
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

func (p *Infoblox) infobloxConnection() (*ibclient.ObjectManager, error) {
	hostConfig := ibclient.HostConfig{
		Host:     p.config.Infoblox.Host,
		Version:  p.config.Infoblox.Version,
		Port:     strconv.Itoa(p.config.Infoblox.Port),
		Username: p.config.Infoblox.Username,
		Password: p.config.Infoblox.Password,
	}
	transportConfig := ibclient.NewTransportConfig("false", 20, 10)
	requestBuilder := &ibclient.WapiRequestBuilder{}
	requestor := &ibclient.WapiHttpRequestor{}

	var objMgr *ibclient.ObjectManager

	if p.config.Override.FakeInfobloxEnabled {
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

//TODO: refactor into common
func (p *Infoblox) nsServerName() string {
	dnsZoneIntoNS := strings.ReplaceAll(p.config.DNSZone, ".", "-")
	return fmt.Sprintf("gslb-ns-%s-%s.%s",
		dnsZoneIntoNS,
		p.config.ClusterGeoTag,
		p.config.EdgeDNSZone)
}

func (p *Infoblox) checkZoneDelegated(findZone *ibclient.ZoneDelegated, gslbZoneName string) error {
	if findZone.Fqdn != gslbZoneName {
		err := fmt.Errorf("delegated zone returned from infoblox(%s) does not match requested gslb zone(%s)", findZone.Fqdn, gslbZoneName)
		return err
	}
	return nil
}

func (p *Infoblox) filterOutDelegateTo(delegateTo []ibclient.NameServer, fqdn string) []ibclient.NameServer {
	for i := 0; i < len(delegateTo); i++ {
		if delegateTo[i].Name == fqdn {
			delegateTo = append(delegateTo[:i], delegateTo[i+1:]...)
			i--
		}
	}
	return delegateTo
}

func (p *Infoblox) getExternalClusterHeartbeatFQDNs() (extGslbClusters []string) {
	for _, geoTag := range p.config.ExtClustersGeoTags {
		extGslbClusters = append(extGslbClusters, fmt.Sprintf("%s-heartbeat-%s.%s", p.gslb.Name, geoTag, p.config.EdgeDNSZone))
	}
	return
}

func (p *Infoblox) checkAliveFromTXT(fqdn string, splitBrainThreshold time.Duration) error {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(fqdn), dns.TypeTXT)
	ns := utils.OverrideWithFakeDNS(p.config.Override.FakeDNSEnabled, p.config.EdgeDNSServer)
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
