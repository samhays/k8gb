package dns

import (
	"fmt"
	"strings"

	"github.com/AbsaOSS/k8gb/controllers/depresolver"
)

func nsServerName(config depresolver.Config) string {
	dnsZoneIntoNS := strings.ReplaceAll(config.DNSZone, ".", "-")
	return fmt.Sprintf("gslb-ns-%s-%s.%s", dnsZoneIntoNS, config.ClusterGeoTag, config.EdgeDNSZone)
}

// TODO: refactor with nsServername
// TODO: tests
// TODO: maybe to internal folder
func nsServerNameExt(config depresolver.Config) []string {
	dnsZoneIntoNS := strings.ReplaceAll(config.DNSZone, ".", "-")
	var extNSServers []string
	for _, clusterGeoTag := range config.ExtClustersGeoTags {
		extNSServers = append(extNSServers,
			fmt.Sprintf("gslb-ns-%s-%s.%s", dnsZoneIntoNS, clusterGeoTag, config.EdgeDNSZone))
	}
	return extNSServers
}
