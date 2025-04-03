package libnetwork

import (
	"context"
	"slices"
	"testing"

	"github.com/docker/docker/libnetwork/config"
	"gotest.tools/v3/assert"
)

func TestNetworkStore(t *testing.T) {
	configOption := config.OptionDataDir(t.TempDir())
	c, err := New(configOption)
	assert.NilError(t, err)
	defer c.Stop()

	// Insert a first network
	nw1 := &Network{id: "testNetwork1", configFrom: "config-network"}
	err = c.storeNetwork(context.Background(), nw1)
	assert.NilError(t, err)

	// Then a second network
	nw2 := &Network{id: "testNetwork2"}
	err = c.storeNetwork(context.Background(), nw2)
	assert.NilError(t, err)

	netSorter := func(a, b *Network) int {
		if a.name < b.name {
			return -1
		}
		if a.name > b.name {
			return 1
		}
		return 0
	}

	for _, tc := range []struct {
		name        string
		filter      func(nw *Network) bool
		expNetworks []*Network
	}{
		{
			name:        "no filter",
			filter:      func(nw *Network) bool { return true },
			expNetworks: []*Network{nw1, nw2},
		},
		{
			name:        "filter by configFrom",
			filter:      filterNetworkByConfigFrom("config-network"),
			expNetworks: []*Network{nw1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := c.findNetworks(tc.filter)
			assert.Equal(t, len(found), len(tc.expNetworks))
			slices.SortFunc(found, netSorter)
			for i, nw := range tc.expNetworks {
				assert.Check(t, found[i] == nw, "got: %s; expected: %s", found[i].name, nw.name)
			}
		})
	}

	// Delete the first network
	err = c.deleteStoredNetwork(nw1)
	assert.NilError(t, err)

	// Check that we can only find the second network
	found := c.findNetworks(func(nw *Network) bool { return true })
	assert.Equal(t, len(found), 1)
	assert.Check(t, found[0] == nw2)

	// Store the second network again
	err = c.storeNetwork(context.Background(), nw2)
	assert.NilError(t, err)
}
