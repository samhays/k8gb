package utils

import (
	"fmt"
	"sort"
	"strings"

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
