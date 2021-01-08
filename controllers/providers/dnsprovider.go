package providers

import (
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// IDnsProvider interface
type IDnsProvider interface {
	// ConfigureZoneDelegation creates zone delegation records for external DNS
	ConfigureZoneDelegation() (*reconcile.Result, error)
	// Finalize Gslb finalizer
	Finalize() error
}
