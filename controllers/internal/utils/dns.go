package utils

import (
	"fmt"
	"sort"

	"github.com/lixiangzhong/dnsutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// TODO: remove log and keep logging on the caller
var log = logf.Log.WithName("controller_gslb")

// Dig digs
func Dig(edgeDNSServer, fqdn string) ([]string, error) {
	var dig dnsutil.Dig
	if edgeDNSServer == "" {
		return nil, fmt.Errorf("empty edgeDNSServer")
	}
	err := dig.SetDNS(edgeDNSServer)
	if err != nil {
		log.Info(fmt.Sprintf("Can't set query dns (%s) with error(%s)", edgeDNSServer, err))
		return nil, err
	}
	a, err := dig.A(fqdn)
	if err != nil {
		log.Info(fmt.Sprintf("Can't dig fqdn(%s) with error(%s)", fqdn, err))
		return nil, err
	}
	var IPs []string
	for _, ip := range a {
		IPs = append(IPs, fmt.Sprint(ip.A))
	}
	sort.Strings(IPs)
	return IPs, nil
}
