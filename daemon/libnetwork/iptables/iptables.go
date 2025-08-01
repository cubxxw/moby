//go:build linux

package iptables

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containerd/log"
	"github.com/moby/moby/v2/daemon/internal/rootless"
)

// Action signifies the iptable action.
type Action string

const (
	// Append appends the rule at the end of the chain.
	Append Action = "-A"
	// Delete deletes the rule from the chain.
	Delete Action = "-D"
	// Insert inserts the rule at the top of the chain.
	Insert Action = "-I"
)

// Policy is the default iptable policies
type Policy string

const (
	// Drop is the default iptables DROP policy.
	Drop Policy = "DROP"
	// Accept is the default iptables ACCEPT policy.
	Accept Policy = "ACCEPT"
)

// Table refers to Nat, Filter or Mangle.
type Table string

const (
	// Nat table is used for nat translation rules.
	Nat Table = "nat"
	// Filter table is used for filter rules.
	Filter Table = "filter"
	// Mangle table is used for mangling the packet.
	Mangle Table = "mangle"
	// Raw table is used for filtering packets before they are NATed.
	Raw Table = "raw"
)

// IPVersion refers to IP version, v4 or v6
type IPVersion string

const (
	// IPv4 is version 4.
	IPv4 IPVersion = "ipv4"
	// IPv6 is version 6.
	IPv6 IPVersion = "ipv6"
)

var (
	iptablesPath  string
	ip6tablesPath string
	initOnce      sync.Once
)

// IPTable defines struct with [IPVersion].
type IPTable struct {
	ipVersion IPVersion
}

// ChainInfo defines the iptables chain.
type ChainInfo struct {
	Name      string
	Table     Table
	IPVersion IPVersion
}

// ChainError is returned to represent errors during ip table operation.
type ChainError struct {
	Chain  string
	Output []byte
}

func (e ChainError) Error() string {
	return fmt.Sprintf("error iptables %s: %s", e.Chain, string(e.Output))
}

// loopbackAddress returns the loopback address for the given IP version.
func loopbackAddress(version IPVersion) string {
	switch version {
	case IPv4, "":
		// IPv4 (default for backward-compatibility)
		return "127.0.0.0/8"
	case IPv6:
		return "::1/128"
	default:
		panic("unknown IP version: " + version)
	}
}

func detectIptables() {
	path, err := exec.LookPath("iptables")
	if err != nil {
		log.G(context.TODO()).WithError(err).Warnf("failed to find iptables")
		return
	}
	iptablesPath = path

	path, err = exec.LookPath("ip6tables")
	if err != nil {
		log.G(context.TODO()).WithError(err).Warnf("unable to find ip6tables")
	} else {
		ip6tablesPath = path
	}
}

func initFirewalld() {
	// When running with RootlessKit, firewalld is running as the root outside our network namespace
	// https://github.com/moby/moby/issues/43781
	if rootless.RunningWithRootlessKit() {
		log.G(context.TODO()).Info("skipping firewalld management for rootless mode")
		return
	}
	if err := firewalldInit(); err != nil {
		log.G(context.TODO()).WithError(err).Debugf("unable to initialize firewalld; using raw iptables instead")
	}
}

func initDependencies() {
	initFirewalld()
	detectIptables()
}

func initCheck() error {
	initOnce.Do(initDependencies)

	if iptablesPath == "" {
		return errors.New("iptables not found")
	}
	return nil
}

// GetIptable returns an instance of IPTable with specified version ([IPv4]
// or [IPv6]). It panics if an invalid [IPVersion] is provided.
func GetIptable(version IPVersion) *IPTable {
	switch version {
	case IPv4, IPv6:
		// valid version
	case "":
		// default is IPv4 for backward-compatibility
		version = IPv4
	default:
		panic("unknown IP version: " + version)
	}
	return &IPTable{ipVersion: version}
}

// NewChain adds a new chain to ip table.
func (iptable IPTable) NewChain(name string, table Table) (*ChainInfo, error) {
	if name == "" {
		return nil, errors.New("could not create chain: chain name is empty")
	}
	if table == "" {
		return nil, fmt.Errorf("could not create chain %s: invalid table name: table name is empty", name)
	}
	// Add chain if it doesn't exist
	if _, err := iptable.Raw("-t", string(table), "-n", "-L", name); err != nil {
		if output, err := iptable.Raw("-t", string(table), "-N", name); err != nil {
			return nil, err
		} else if len(output) != 0 {
			return nil, fmt.Errorf("could not create %s/%s chain: %s", table, name, output)
		}
	}
	return &ChainInfo{
		Name:      name,
		Table:     table,
		IPVersion: iptable.ipVersion,
	}, nil
}

// RemoveExistingChain removes existing chain from the table.
func (iptable IPTable) RemoveExistingChain(name string, table Table) error {
	if name == "" {
		return errors.New("could not remove chain: chain name is empty")
	}
	if table == "" {
		return fmt.Errorf("could not remove chain %s: invalid table name: table name is empty", name)
	}
	c := &ChainInfo{
		Name:      name,
		Table:     table,
		IPVersion: iptable.ipVersion,
	}
	return c.Remove()
}

// Link adds reciprocal ACCEPT rule for two supplied IP addresses.
// Traffic is allowed from ip1 to ip2 and vice-versa
func (c *ChainInfo) Link(action Action, ip1, ip2 netip.Addr, port int, proto string, bridgeName string) error {
	iptable := GetIptable(c.IPVersion)
	// forward
	args := []string{
		"-i", bridgeName, "-o", bridgeName,
		"-p", proto,
		"-s", ip1.String(),
		"-d", ip2.String(),
		"--dport", strconv.Itoa(port),
		"-j", "ACCEPT",
	}

	if err := iptable.ProgramRule(Filter, c.Name, action, args); err != nil {
		return err
	}
	// reverse
	args[7], args[9] = args[9], args[7]
	args[10] = "--sport"
	return iptable.ProgramRule(Filter, c.Name, action, args)
}

// ProgramRule adds the rule specified by args only if the
// rule is not already present in the chain. Reciprocally,
// it removes the rule only if present.
func (iptable IPTable) ProgramRule(table Table, chain string, action Action, args []string) error {
	if iptable.Exists(table, chain, args...) != (action == Delete) {
		return nil
	}
	return iptable.RawCombinedOutput(append([]string{"-t", string(table), string(action), chain}, args...)...)
}

// Prerouting adds linking rule to nat/PREROUTING chain.
func (c *ChainInfo) Prerouting(action Action, args ...string) error {
	iptable := GetIptable(c.IPVersion)
	a := []string{"-t", string(Nat), string(action), "PREROUTING"}
	if len(args) > 0 {
		a = append(a, args...)
	}
	if output, err := iptable.Raw(a...); err != nil {
		return err
	} else if len(output) != 0 {
		return ChainError{Chain: "PREROUTING", Output: output}
	}
	return nil
}

// Output adds linking rule to an OUTPUT chain.
func (c *ChainInfo) Output(action Action, args ...string) error {
	a := []string{"-t", string(c.Table), string(action), "OUTPUT"}
	if len(args) > 0 {
		a = append(a, args...)
	}
	if output, err := GetIptable(c.IPVersion).Raw(a...); err != nil {
		return err
	} else if len(output) != 0 {
		return ChainError{Chain: "OUTPUT", Output: output}
	}
	return nil
}

// Remove removes the chain.
func (c *ChainInfo) Remove() error {
	// Ignore errors - This could mean the chains were never set up
	if c.Table == Nat {
		_ = c.Prerouting(Delete, "-m", "addrtype", "--dst-type", "LOCAL", "-j", c.Name)
		_ = c.Output(Delete, "-m", "addrtype", "--dst-type", "LOCAL", "!", "--dst", loopbackAddress(c.IPVersion), "-j", c.Name)
		_ = c.Output(Delete, "-m", "addrtype", "--dst-type", "LOCAL", "-j", c.Name) // Created in versions <= 0.1.6
		_ = c.Prerouting(Delete)
		_ = c.Output(Delete)
	}
	iptable := GetIptable(c.IPVersion)
	_, _ = iptable.Raw("-t", string(c.Table), "-F", c.Name)
	_, _ = iptable.Raw("-t", string(c.Table), "-X", c.Name)
	return nil
}

// Exists checks if a rule exists
func (iptable IPTable) Exists(table Table, chain string, rule ...string) bool {
	return iptable.exists(false, table, chain, rule...)
}

// ExistsNative behaves as Exists with the difference it
// will always invoke `iptables` binary.
func (iptable IPTable) ExistsNative(table Table, chain string, rule ...string) bool {
	return iptable.exists(true, table, chain, rule...)
}

func (iptable IPTable) exists(native bool, table Table, chain string, rule ...string) bool {
	if err := initCheck(); err != nil {
		// The exists() signature does not allow us to return an error, but at least
		// we can skip the (likely invalid) exec invocation.
		return false
	}

	f := iptable.Raw
	if native {
		f = iptable.raw
	}

	if table == "" {
		table = Filter
	}

	// if exit status is 0 then return true, the rule exists
	_, err := f(append([]string{"-t", string(table), "-C", chain}, rule...)...)
	return err == nil
}

const (
	// opWarnTime is the maximum duration that an iptables operation can take before flagging a warning.
	opWarnTime = 2 * time.Second

	// xLockWaitMsg is the iptables warning about xtables lock that can be suppressed.
	xLockWaitMsg = "Another app is currently holding the xtables lock"
)

func filterOutput(start time.Time, output []byte, args ...string) []byte {
	if opTime := time.Since(start); opTime > opWarnTime {
		// Flag operations that have taken a long time to complete
		log.G(context.TODO()).Warnf("xtables contention detected while running [%s]: Waited for %.2f seconds and received %q", strings.Join(args, " "), float64(opTime)/float64(time.Second), string(output))
	}
	// ignore iptables' message about xtables lock:
	// it is a warning, not an error.
	if strings.Contains(string(output), xLockWaitMsg) {
		output = []byte("")
	}
	// Put further filters here if desired
	return output
}

// Raw calls 'iptables' system command, passing supplied arguments.
func (iptable IPTable) Raw(args ...string) ([]byte, error) {
	if firewalldRunning {
		startTime := time.Now()
		output, err := passthrough(iptable.ipVersion, args...)
		if err == nil || !strings.Contains(err.Error(), "was not provided by any .service files") {
			return filterOutput(startTime, output, args...), err
		}
	}
	return iptable.raw(args...)
}

func (iptable IPTable) raw(args ...string) ([]byte, error) {
	if err := initCheck(); err != nil {
		return nil, err
	}
	path := iptablesPath
	commandName := "iptables"
	if iptable.ipVersion == IPv6 {
		if ip6tablesPath == "" {
			return nil, errors.New("ip6tables is missing")
		}
		path = ip6tablesPath
		commandName = "ip6tables"
	}

	args = append([]string{"--wait"}, args...)
	log.G(context.TODO()).Debugf("%s, %v", path, args)

	startTime := time.Now()
	output, err := exec.Command(path, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("iptables failed: %s %v: %s (%s)", commandName, strings.Join(args, " "), output, err)
	}

	return filterOutput(startTime, output, args...), err
}

// RawCombinedOutput internally calls the Raw function and returns a non nil
// error if Raw returned a non nil error or a non empty output
func (iptable IPTable) RawCombinedOutput(args ...string) error {
	if output, err := iptable.Raw(args...); err != nil || len(output) != 0 {
		return fmt.Errorf("%s (%v)", string(output), err)
	}
	return nil
}

// RawCombinedOutputNative behave as RawCombinedOutput with the difference it
// will always invoke `iptables` binary
func (iptable IPTable) RawCombinedOutputNative(args ...string) error {
	if output, err := iptable.raw(args...); err != nil || len(output) != 0 {
		return fmt.Errorf("%s (%v)", string(output), err)
	}
	return nil
}

// ExistChain checks if a chain exists
func (iptable IPTable) ExistChain(chain string, table Table) bool {
	_, err := iptable.Raw("-t", string(table), "-nL", chain)
	return err == nil
}

// FlushChain flush chain if it exists
func (iptable IPTable) FlushChain(table Table, chain string) error {
	if !iptable.ExistChain(chain, table) {
		return nil
	}
	return iptable.RawCombinedOutput("-t", string(table), "-F", chain)
}

// SetDefaultPolicy sets the passed default policy for the table/chain
func (iptable IPTable) SetDefaultPolicy(table Table, chain string, policy Policy) error {
	if err := iptable.RawCombinedOutput("-t", string(table), "-P", chain, string(policy)); err != nil {
		return fmt.Errorf("setting default policy to %v in %v chain failed: %v", policy, chain, err)
	}
	return nil
}

// AddReturnRule adds a return rule for the chain in the filter table
func (iptable IPTable) AddReturnRule(table Table, chain string) error {
	if iptable.Exists(table, chain, "-j", "RETURN") {
		return nil
	}
	if err := iptable.RawCombinedOutput("-t", string(table), "-A", chain, "-j", "RETURN"); err != nil {
		return fmt.Errorf("unable to add return rule in %s chain: %v", chain, err)
	}
	return nil
}

// EnsureJumpRule ensures the jump rule is on top
func (iptable IPTable) EnsureJumpRule(table Table, fromChain, toChain string, rule ...string) error {
	if err := iptable.DeleteJumpRule(table, fromChain, toChain, rule...); err != nil {
		return err
	}
	rule = append(rule, "-j", toChain)
	if err := iptable.RawCombinedOutput(append([]string{"-t", string(table), "-I", fromChain}, rule...)...); err != nil {
		return fmt.Errorf("unable to insert jump to %s rule in %s chain: %v", toChain, fromChain, err)
	}
	return nil
}

// DeleteJumpRule deletes a rule added by EnsureJumpRule. It's a no-op if the rule
// doesn't exist.
func (iptable IPTable) DeleteJumpRule(table Table, fromChain, toChain string, rule ...string) error {
	rule = append(rule, "-j", toChain)
	if iptable.Exists(table, fromChain, rule...) {
		if err := iptable.RawCombinedOutput(append([]string{"-t", string(table), "-D", fromChain}, rule...)...); err != nil {
			return fmt.Errorf("unable to remove jump to %s rule in %s chain: %v", toChain, fromChain, err)
		}
	}
	return nil
}

type Rule struct {
	IPVer IPVersion
	Table Table
	Chain string
	Args  []string
}

// Exists returns true if the rule exists in the kernel.
func (r Rule) Exists() bool {
	return GetIptable(r.IPVer).Exists(r.Table, r.Chain, r.Args...)
}

func (r Rule) cmdArgs(op Action) []string {
	return append([]string{"-t", string(r.Table), string(op), r.Chain}, r.Args...)
}

func (r Rule) exec(op Action) error {
	return GetIptable(r.IPVer).RawCombinedOutput(r.cmdArgs(op)...)
}

// WithChain returns a version of the rule with its Chain field set to chain.
func (r Rule) WithChain(chain string) Rule {
	wc := r
	wc.Args = slices.Clone(r.Args)
	wc.Chain = chain
	return wc
}

// ensure appends/insert the rule to the end of the chain. If the rule already exists anywhere in the
// chain, this is a no-op.
func (r Rule) ensure(op Action) error {
	if r.Exists() {
		return nil
	}
	return r.exec(op)
}

// Append appends the rule to the end of the chain. If the rule already exists anywhere in the
// chain, this is a no-op.
func (r Rule) Append() error {
	return r.ensure(Append)
}

// Insert inserts the rule at the head of the chain. If the rule already exists anywhere in the
// chain, this is a no-op.
func (r Rule) Insert() error {
	return r.ensure(Insert)
}

// Delete deletes the rule from the kernel. If the rule does not exist, this is a no-op.
func (r Rule) Delete() error {
	if !r.Exists() {
		return nil
	}
	return r.exec(Delete)
}

func (r Rule) String() string {
	cmd := append([]string{"iptables"}, r.cmdArgs("-A")...)
	if r.IPVer == IPv6 {
		cmd[0] = "ip6tables"
	}
	return strings.Join(cmd, " ")
}
