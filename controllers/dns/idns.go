package dns

import (
	k8gbv1beta1 "github.com/AbsaOSS/k8gb/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type IDnsProvider interface {
	HandleZoneDelegation(gslb *k8gbv1beta1.Gslb) (*reconcile.Result, error)
	Finalize(gslb *k8gbv1beta1.Gslb) error
}
