package utils

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	v1beta1 "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	types "k8s.io/apimachinery/pkg/types"

	"github.com/lixiangzhong/dnsutil"
)

// Dig returns the list of IPs
func Dig(edgeDNSServer, fqdn string) ([]string, error) {
	var dig dnsutil.Dig
	if edgeDNSServer == "" {
		return nil, fmt.Errorf("empty edgeDNSServer")
	}
	err := dig.SetDNS(edgeDNSServer)
	if err != nil {
		err = fmt.Errorf("can't set query dns (%s) with error(%w)", edgeDNSServer, err)
		return nil, err
	}
	a, err := dig.A(fqdn)
	if err != nil {
		err = fmt.Errorf("can't dig fqdn(%s) with error(%w)", fqdn, err)
		return nil, err
	}
	var IPs []string
	for _, ip := range a {
		IPs = append(IPs, fmt.Sprint(ip.A))
	}
	sort.Strings(IPs)
	return IPs, nil
}

// NsServerNameExt retrieves list of external GSLB clusters
//TODO: guards
func NsServerNameExt(dnsZone, edgeDNSZone string, extClusterGeoTags []string) []string {
	dnsZoneIntoNS := strings.ReplaceAll(dnsZone, ".", "-")
	var extNSServers []string
	for _, clusterGeoTag := range extClusterGeoTags {
		extNSServers = append(extNSServers,
			fmt.Sprintf("gslb-ns-%s-%s.%s",
				dnsZoneIntoNS,
				clusterGeoTag,
				edgeDNSZone))
	}
	return extNSServers
}

func GetExternalClusterHeartbeatFQDNs(gslb *k8gbv1beta1.Gslb, edgeDNSZone string, extClusterGeoTags []string) (extGslbClusters []string) {
	for _, geoTag := range extClusterGeoTags {
		extGslbClusters = append(extGslbClusters, fmt.Sprintf("%s-heartbeat-%s.%s", gslb.Name, geoTag, edgeDNSZone))
	}
	return
}

func OverrideWithFakeDNS(fakeDNSEnabled bool, server string) (ns string) {
	if fakeDNSEnabled {
		ns = "127.0.0.1:7753"
	} else {
		ns = fmt.Sprintf("%s:53", server)
	}
	return
}

func GetGslbIngressIPs(gslb *k8gbv1beta1.Gslb, client client.Client, edgeDNSServer string) ([]string, error) {
	nn := types.NamespacedName{
		Name:      gslb.Name,
		Namespace: gslb.Namespace,
	}

	gslbIngress := &v1beta1.Ingress{}

	err := client.Get(context.TODO(), nn, gslbIngress)
	if err != nil {
		if errors.IsNotFound(err) {
			err = fmt.Errorf("can't find gslb Ingress: %s", gslb.Name)
		}
		return nil, err
	}

	var gslbIngressIPs []string

	for _, ip := range gslbIngress.Status.LoadBalancer.Ingress {
		if len(ip.IP) > 0 {
			gslbIngressIPs = append(gslbIngressIPs, ip.IP)
		}
		if len(ip.Hostname) > 0 {
			IPs, err := Dig(edgeDNSServer, ip.Hostname)
			if err != nil {
				err = fmt.Errorf("can't dig %s; %w", edgeDNSServer, err)
				return nil, err
			}
			gslbIngressIPs = append(gslbIngressIPs, IPs...)
		}
	}
	return gslbIngressIPs, nil
}

// CoreDNSExposedIPs retrieves exposed IPs of CoreDNS service
func CoreDNSExposedIPs(client client.Client, edgeDNSServer, k8gbNamespace, coreDNSExtServiceName string) ([]string, error) {
	coreDNSService := &corev1.Service{}

	err := client.Get(context.TODO(), types.NamespacedName{Namespace: k8gbNamespace, Name: coreDNSExtServiceName}, coreDNSService)
	if err != nil {
		if errors.IsNotFound(err) {
			err = fmt.Errorf("can't find %s service' %w", coreDNSExtServiceName, err)
		}
		return nil, err
	}
	var lbHostname string
	if len(coreDNSService.Status.LoadBalancer.Ingress) > 0 {
		lbHostname = coreDNSService.Status.LoadBalancer.Ingress[0].Hostname
	} else {
		err := fmt.Errorf("no Ingress LoadBalancer entries found for %s serice", coreDNSExtServiceName)
		return nil, err
	}
	IPs, err := Dig(edgeDNSServer, lbHostname)
	if err != nil {
		err = fmt.Errorf("can't dig k8gb-coredns-lb service loadbalancer fqdn %s; %w", lbHostname, err)
		return nil, err
	}
	return IPs, nil
}
