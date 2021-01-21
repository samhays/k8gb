package dns

import (
	"github.com/AbsaOSS/k8gb/controllers/depresolver"

	"testing"

	"github.com/stretchr/testify/assert"
)


var predefinedConfig = depresolver.Config{
	ClusterGeoTag:           "us",
	DNSZone:                 "example.com",
	ExtClustersGeoTags:      []string{"uk", "eu"},
	EdgeDNSZone:             "8.8.8.8",
}

func TestNsServerName(t *testing.T) {
	// arrange
	// act
	result := nsServerName(predefinedConfig)
	// assert
	assert.Equal(t, "gslb-ns-example-com-us.8.8.8.8", result)
}

func TestEmptyClusterGeoTagNSServerName(t *testing.T) {
	// arrange
	config := predefinedConfig
	config.ClusterGeoTag = ""
	// act
	result := nsServerName(config)
	// assert
	assert.Equal(t, "gslb-ns-example-com-.8.8.8.8", result)
}

func TestNsServerNameExt(t *testing.T) {
	// arrange
	expected := []string{"gslb-ns-example-com-uk.8.8.8.8", "gslb-ns-example-com-eu.8.8.8.8"}
	// act
	result := nsServerNameExt(predefinedConfig)
	// assert
	assert.Equal(t, expected, result)
}

func TestNsServerNameExtWithEmptyGeoTag(t *testing.T) {
	// arrange
	config := predefinedConfig
	config.ExtClustersGeoTags = []string{}
	// act
	result := nsServerNameExt(config)
	// assert
	assert.Equal(t, []string{}, result)
}
