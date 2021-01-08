package controllers

import (
	"context"
	"fmt"

	"github.com/AbsaOSS/k8gb/controllers/providers/infoblox"

	"github.com/AbsaOSS/k8gb/controllers/depresolver"

	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	externaldns "sigs.k8s.io/external-dns/endpoint"
)

func (r *GslbReconciler) finalizeGslb(gslb *k8gbv1beta1.Gslb) error {
	// needs to do before the CR can be deleted. Examples
	// of finalizers include performing backups and deleting
	// resources that are not owned by this CR, like a PVC.

	if r.Config.EdgeDNSType == depresolver.DNSTypeRoute53 {
		log.Info("Removing Zone Delegation entries...")
		dnsEndpointRoute53 := &externaldns.DNSEndpoint{}
		err := r.Get(context.Background(), client.ObjectKey{Namespace: k8gbNamespace, Name: "k8gb-ns-route53"}, dnsEndpointRoute53)
		if err != nil {
			if errors.IsNotFound(err) {
				log.Info(fmt.Sprint(err))
				return nil
			}
			return err
		}
		err = r.Delete(context.Background(), dnsEndpointRoute53)
		if err != nil {
			return err
		}
	}

	if r.Config.EdgeDNSType == depresolver.DNSTypeInfoblox {
		provider, err := infoblox.NewInfoblox(r.Config, gslb, r.Client)
		if err != nil {
			return err
		}
		return provider.Finalize()
	}

	log.Info("Successfully finalized Gslb")
	return nil
}

func (r *GslbReconciler) addFinalizer(gslb *k8gbv1beta1.Gslb) error {
	log.Info("Adding Finalizer for the Gslb")
	gslb.SetFinalizers(append(gslb.GetFinalizers(), gslbFinalizer))

	// Update CR
	err := r.Update(context.TODO(), gslb)
	if err != nil {
		log.Error(err, "Failed to update Gslb with finalizer")
		return err
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func remove(list []string, s string) []string {
	for i, v := range list {
		if v == s {
			list = append(list[:i], list[i+1:]...)
		}
	}
	return list
}
