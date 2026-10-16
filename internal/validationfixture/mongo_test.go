package validationfixture

import (
	"strings"
	"testing"
)

func TestMongoURIRejectsRedirectsAndSharedTargets(t *testing.T) {
	for _, raw := range []string{
		"mongodb://remote", "mongodb://user:pass@localhost", "mongodb://localhost:27017,remote:27017",
		"mongodb://localhost,remote", "mongodb://localhost/production", "mongodb://localhost#fragment",
		"mongodb://localhost/?proxyHost=remote", "mongodb://localhost/?replicaSet=shared",
		"mongodb://localhost/?directConnection=false", "mongodb://localhost:0", "mongodb://localhost:70000",
		"mongodb://[", "mongodb://localhost:invalid",
	} {
		if _, err := MongoURI(raw); err == nil {
			t.Errorf("accepted unsafe fixture URI %q", raw)
		}
	}
}

func TestMongoURIDisablesReplicaDiscovery(t *testing.T) {
	for _, raw := range []string{"mongodb://127.0.0.1:27017", "mongodb://localhost/", "mongodb://[::1]:27017/?directConnection=true"} {
		got, err := MongoURI(raw)
		if err != nil || !strings.HasSuffix(got, "?directConnection=true") {
			t.Fatalf("normalize %q = %q, %v", raw, got, err)
		}
	}
}
