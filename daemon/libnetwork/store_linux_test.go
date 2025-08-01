package libnetwork

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/moby/moby/v2/daemon/libnetwork/config"
	store "github.com/moby/moby/v2/daemon/libnetwork/internal/kvstore"
)

func TestBoltdbBackend(t *testing.T) {
	tmpPath := filepath.Join(t.TempDir(), "boltdb.db")
	testLocalBackend(t, tmpPath, "testBackend")
}

func TestNoPersist(t *testing.T) {
	configOption := config.OptionDataDir(t.TempDir())
	testController, err := New(context.Background(), configOption)
	if err != nil {
		t.Fatalf("Error creating new controller: %v", err)
	}
	defer testController.Stop()
	nw, err := testController.NewNetwork(context.Background(), "host", "host", "", NetworkOptionPersist(false))
	if err != nil {
		t.Fatalf(`Error creating default "host" network: %v`, err)
	}
	ep, err := nw.CreateEndpoint(context.Background(), "newendpoint", []EndpointOption{}...)
	if err != nil {
		t.Fatalf("Error creating endpoint: %v", err)
	}
	testController.Stop()

	// Create a new controller using the same database-file. The network
	// should not have persisted.
	testController, err = New(context.Background(), configOption)
	if err != nil {
		t.Fatalf("Error creating new controller: %v", err)
	}
	defer testController.Stop()

	nwKVObject := &Network{id: nw.ID()}
	err = testController.store.GetObject(nwKVObject)
	if !errors.Is(err, store.ErrKeyNotFound) {
		t.Errorf("Expected %q error when retrieving network from store, got: %q", store.ErrKeyNotFound, err)
	}
	if nwKVObject.Exists() {
		t.Errorf("Network with persist=false should not be stored in KV Store")
	}

	epKVObject := &Endpoint{network: nw, id: ep.ID()}
	err = testController.store.GetObject(epKVObject)
	if !errors.Is(err, store.ErrKeyNotFound) {
		t.Errorf("Expected %v error when retrieving endpoint from store, got: %v", store.ErrKeyNotFound, err)
	}
	if epKVObject.Exists() {
		t.Errorf("Endpoint in Network with persist=false should not be stored in KV Store")
	}
}
