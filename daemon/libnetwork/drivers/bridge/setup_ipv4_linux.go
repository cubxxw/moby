package bridge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/containerd/log"
	"github.com/moby/moby/v2/daemon/libnetwork/types"
	"github.com/vishvananda/netlink"
)

func selectIPv4Address(addresses []netlink.Addr, selector *net.IPNet) (netlink.Addr, error) {
	if len(addresses) == 0 {
		return netlink.Addr{}, errors.New("unable to select an address as the address pool is empty")
	}
	if selector != nil {
		for _, addr := range addresses {
			if selector.Contains(addr.IP) {
				return addr, nil
			}
		}
	}
	return addresses[0], nil
}

func setupBridgeIPv4(config *networkConfiguration, i *bridgeInterface) error {
	// TODO(aker): the bridge driver panics if its bridgeIPv4 field isn't set. Once bridge subnet and bridge IP address
	//             are decoupled, we should assign it only when it's really needed.
	i.bridgeIPv4 = config.AddressIPv4

	if !config.InhibitIPv4 && !config.GwModeIPv4.isolated() {
		addrv4List, err := i.addresses(netlink.FAMILY_V4)
		if err != nil {
			return fmt.Errorf("failed to retrieve bridge interface addresses: %v", err)
		}

		addrv4, _ := selectIPv4Address(addrv4List, config.AddressIPv4)

		if !types.CompareIPNet(addrv4.IPNet, config.AddressIPv4) {
			if addrv4.IPNet != nil {
				if err := i.nlh.AddrDel(i.Link, &addrv4); err != nil {
					return fmt.Errorf("failed to remove current ip address from bridge: %v", err)
				}
			}
			log.G(context.TODO()).Debugf("Assigning address to bridge interface %s: %s", config.BridgeName, config.AddressIPv4)
			if err := i.nlh.AddrAdd(i.Link, &netlink.Addr{IPNet: config.AddressIPv4}); err != nil {
				return fmt.Errorf("failed to add IPv4 address %s to bridge: %v", config.AddressIPv4, err)
			}
		}
	}

	if !config.Internal {
		// Store the default gateway
		i.gatewayIPv4 = config.AddressIPv4.IP
	}

	return nil
}

func setupGatewayIPv4(config *networkConfiguration, i *bridgeInterface) error {
	if !i.bridgeIPv4.Contains(config.DefaultGatewayIPv4) {
		return errInvalidGateway
	}
	if config.Internal {
		return types.InvalidParameterErrorf("no gateway can be set on an internal bridge network")
	}

	// Store requested default gateway
	i.gatewayIPv4 = config.DefaultGatewayIPv4

	return nil
}

func setupLoopbackAddressesRouting(config *networkConfiguration, i *bridgeInterface) error {
	sysPath := filepath.Join("/proc/sys/net/ipv4/conf", config.BridgeName, "route_localnet")
	ipv4LoRoutingData, err := os.ReadFile(sysPath)
	if err != nil {
		return fmt.Errorf("Cannot read IPv4 local routing setup: %v", err)
	}
	// Enable loopback addresses routing only if it isn't already enabled
	if ipv4LoRoutingData[0] != '1' {
		if err := os.WriteFile(sysPath, []byte{'1', '\n'}, 0o644); err != nil {
			return fmt.Errorf("Unable to enable local routing for hairpin mode: %v", err)
		}
	}
	return nil
}
