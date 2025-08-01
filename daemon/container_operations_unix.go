//go:build linux || freebsd

package daemon

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/containerd/log"
	"github.com/moby/moby/v2/daemon/config"
	"github.com/moby/moby/v2/daemon/container"
	"github.com/moby/moby/v2/daemon/internal/stringid"
	"github.com/moby/moby/v2/daemon/libnetwork"
	"github.com/moby/moby/v2/daemon/libnetwork/drivers/bridge"
	"github.com/moby/moby/v2/daemon/links"
	"github.com/moby/moby/v2/daemon/network"
	"github.com/moby/moby/v2/errdefs"
	"github.com/moby/moby/v2/pkg/process"
	"github.com/moby/sys/mount"
	"github.com/moby/sys/user"
	"github.com/opencontainers/selinux/go-selinux/label"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"golang.org/x/sys/unix"
)

func (daemon *Daemon) setupLinkedContainers(ctr *container.Container) ([]string, error) {
	bridgeSettings := ctr.NetworkSettings.Networks[network.DefaultNetwork]
	if bridgeSettings == nil || bridgeSettings.EndpointSettings == nil {
		return nil, nil
	}

	var env []string
	for linkAlias, child := range daemon.linkIndex.children(ctr) {
		if !child.IsRunning() {
			return nil, fmt.Errorf("Cannot link to a non running container: %s AS %s", child.Name, linkAlias)
		}

		childBridgeSettings := child.NetworkSettings.Networks[network.DefaultNetwork]
		if childBridgeSettings == nil || childBridgeSettings.EndpointSettings == nil {
			return nil, fmt.Errorf("container %s not attached to default bridge network", child.ID)
		}

		linkEnvVars := links.EnvVars(
			bridgeSettings.IPAddress,
			childBridgeSettings.IPAddress,
			linkAlias,
			child.Config.Env,
			child.Config.ExposedPorts,
		)

		env = append(env, linkEnvVars...)
	}

	return env, nil
}

func (daemon *Daemon) addLegacyLinks(
	ctx context.Context,
	cfg *config.Config,
	ctr *container.Container,
	epConfig *network.EndpointSettings,
	sb *libnetwork.Sandbox,
) error {
	ctx, span := otel.Tracer("").Start(ctx, "daemon.addLegacyLinks")
	defer span.End()

	if epConfig.EndpointID == "" {
		return nil
	}

	children := daemon.linkIndex.children(ctr)
	var parents map[string]*container.Container
	if !cfg.DisableBridge && ctr.HostConfig.NetworkMode.IsPrivate() {
		parents = daemon.linkIndex.parents(ctr)
	}
	if len(children) == 0 && len(parents) == 0 {
		return nil
	}
	for _, child := range children {
		if _, ok := child.NetworkSettings.Networks[network.DefaultNetwork]; !ok {
			return fmt.Errorf("Cannot link to %s, as it does not belong to the default network", child.Name)
		}
	}

	var (
		childEndpoints []string
		cEndpointID    string
	)
	for linkAlias, child := range children {
		_, alias := path.Split(linkAlias)
		// allow access to the linked container via the alias, real name, and container hostname
		aliasList := alias + " " + child.Config.Hostname
		// only add the name if alias isn't equal to the name
		if alias != child.Name[1:] {
			aliasList = aliasList + " " + child.Name[1:]
		}
		defaultNW := child.NetworkSettings.Networks[network.DefaultNetwork]
		if defaultNW.IPAddress != "" {
			if err := sb.AddHostsEntry(ctx, aliasList, defaultNW.IPAddress); err != nil {
				return errors.Wrapf(err, "failed to add address to /etc/hosts for link to %s", child.Name)
			}
		}
		if defaultNW.GlobalIPv6Address != "" {
			if err := sb.AddHostsEntry(ctx, aliasList, defaultNW.GlobalIPv6Address); err != nil {
				return errors.Wrapf(err, "failed to add IPv6 address to /etc/hosts for link to %s", child.Name)
			}
		}
		cEndpointID = defaultNW.EndpointID
		if cEndpointID != "" {
			childEndpoints = append(childEndpoints, cEndpointID)
		}
	}

	var parentEndpoints []string
	for alias, parent := range parents {
		_, alias = path.Split(alias)
		// Update ctr's IP address in /etc/hosts files in containers with legacy-links to ctr.
		log.G(context.TODO()).Debugf("Update /etc/hosts of %s for alias %s with ip %s", parent.ID, alias, epConfig.IPAddress)
		if psb, _ := daemon.netController.GetSandbox(parent.ID); psb != nil {
			if err := psb.UpdateHostsEntry(alias, epConfig.IPAddress); err != nil {
				return errors.Wrapf(err, "failed to update /etc/hosts of %s for alias %s with IP %s",
					parent.ID, alias, epConfig.IPAddress)
			}
			if epConfig.GlobalIPv6Address != "" {
				if err := psb.UpdateHostsEntry(alias, epConfig.GlobalIPv6Address); err != nil {
					return errors.Wrapf(err, "failed to update /etc/hosts of %s for alias %s with IP %s",
						parent.ID, alias, epConfig.GlobalIPv6Address)
				}
			}
		}
		if cEndpointID != "" {
			parentEndpoints = append(parentEndpoints, cEndpointID)
		}
	}

	sb.UpdateLabels(bridge.LegacyContainerLinkOptions(parentEndpoints, childEndpoints))

	return nil
}

func (daemon *Daemon) getIPCContainer(id string) (*container.Container, error) {
	// Check if the container exists, is running, and not restarting
	ctr, err := daemon.GetContainer(id)
	if err != nil {
		return nil, errdefs.InvalidParameter(err)
	}
	if !ctr.IsRunning() {
		return nil, errNotRunning(id)
	}
	if ctr.IsRestarting() {
		return nil, errContainerIsRestarting(id)
	}

	// Check the container ipc is shareable
	if st, err := os.Stat(ctr.ShmPath); err != nil || !st.IsDir() {
		if err == nil || os.IsNotExist(err) {
			return nil, errdefs.InvalidParameter(errors.New("container " + id + ": non-shareable IPC (hint: use IpcMode:shareable for the donor container)"))
		}
		// stat() failed?
		return nil, errdefs.System(errors.Wrap(err, "container "+id))
	}

	return ctr, nil
}

func (daemon *Daemon) getPIDContainer(id string) (*container.Container, error) {
	ctr, err := daemon.GetContainer(id)
	if err != nil {
		return nil, errdefs.InvalidParameter(err)
	}
	if !ctr.IsRunning() {
		return nil, errNotRunning(id)
	}
	if ctr.IsRestarting() {
		return nil, errContainerIsRestarting(id)
	}

	return ctr, nil
}

// setupContainerDirs sets up base container directories (root, ipc, tmpfs and secrets).
func (daemon *Daemon) setupContainerDirs(ctr *container.Container) (_ []container.Mount, err error) {
	if err := daemon.setupContainerMountsRoot(ctr); err != nil {
		return nil, err
	}

	if err := daemon.setupIPCDirs(ctr); err != nil {
		return nil, err
	}

	if err := daemon.setupSecretDir(ctr); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			daemon.cleanupSecretDir(ctr)
		}
	}()

	var ms []container.Mount
	if !ctr.HostConfig.IpcMode.IsPrivate() && !ctr.HostConfig.IpcMode.IsEmpty() {
		ms = append(ms, ctr.IpcMounts()...)
	}

	tmpfsMounts, err := ctr.TmpfsMounts()
	if err != nil {
		return nil, err
	}
	ms = append(ms, tmpfsMounts...)

	secretMounts, err := ctr.SecretMounts()
	if err != nil {
		return nil, err
	}
	ms = append(ms, secretMounts...)

	return ms, nil
}

func (daemon *Daemon) setupIPCDirs(ctr *container.Container) error {
	ipcMode := ctr.HostConfig.IpcMode

	switch {
	case ipcMode.IsContainer():
		ic, err := daemon.getIPCContainer(ipcMode.Container())
		if err != nil {
			return errors.Wrapf(err, "failed to join IPC namespace")
		}
		ctr.ShmPath = ic.ShmPath

	case ipcMode.IsHost():
		if _, err := os.Stat("/dev/shm"); err != nil {
			return errors.New("/dev/shm is not mounted, but must be for --ipc=host")
		}
		ctr.ShmPath = "/dev/shm"

	case ipcMode.IsPrivate(), ipcMode.IsNone():
		// c.ShmPath will/should not be used, so make it empty.
		// Container's /dev/shm mount comes from OCI spec.
		ctr.ShmPath = ""

	case ipcMode.IsEmpty():
		// A container was created by an older version of the daemon.
		// The default behavior used to be what is now called "shareable".
		fallthrough

	case ipcMode.IsShareable():
		uid, gid := daemon.idMapping.RootPair()
		if !ctr.HasMountFor("/dev/shm") {
			shmPath, err := ctr.ShmResourcePath()
			if err != nil {
				return err
			}

			if err := user.MkdirAllAndChown(shmPath, 0o700, uid, gid); err != nil {
				return err
			}

			shmproperty := "mode=1777,size=" + strconv.FormatInt(ctr.HostConfig.ShmSize, 10)
			if err := unix.Mount("shm", shmPath, "tmpfs", uintptr(unix.MS_NOEXEC|unix.MS_NOSUID|unix.MS_NODEV), label.FormatMountLabel(shmproperty, ctr.GetMountLabel())); err != nil {
				return fmt.Errorf("mounting shm tmpfs: %s", err)
			}
			if err := os.Chown(shmPath, uid, gid); err != nil {
				return err
			}
			ctr.ShmPath = shmPath
		}

	default:
		return fmt.Errorf("invalid IPC mode: %v", ipcMode)
	}

	return nil
}

func (daemon *Daemon) setupSecretDir(ctr *container.Container) (setupErr error) {
	if len(ctr.SecretReferences) == 0 && len(ctr.ConfigReferences) == 0 {
		return nil
	}

	if err := daemon.createSecretsDir(ctr); err != nil {
		return err
	}
	defer func() {
		if setupErr != nil {
			daemon.cleanupSecretDir(ctr)
		}
	}()

	if ctr.DependencyStore == nil {
		return errors.New("secret store is not initialized")
	}

	// retrieve possible remapped range start for root UID, GID
	ruid, rgid := daemon.idMapping.RootPair()

	for _, s := range ctr.SecretReferences {
		// TODO (ehazlett): use type switch when more are supported
		if s.File == nil {
			log.G(context.TODO()).Error("secret target type is not a file target")
			continue
		}

		// secrets are created in the SecretMountPath on the host, at a
		// single level
		fPath, err := ctr.SecretFilePath(*s)
		if err != nil {
			return errors.Wrap(err, "error getting secret file path")
		}
		if err := user.MkdirAllAndChown(filepath.Dir(fPath), 0o700, ruid, rgid); err != nil {
			return errors.Wrap(err, "error creating secret mount path")
		}

		log.G(context.TODO()).WithFields(log.Fields{
			"name": s.File.Name,
			"path": fPath,
		}).Debug("injecting secret")
		secret, err := ctr.DependencyStore.Secrets().Get(s.SecretID)
		if err != nil {
			return errors.Wrap(err, "unable to get secret from secret store")
		}
		if err := os.WriteFile(fPath, secret.Spec.Data, s.File.Mode); err != nil {
			return errors.Wrap(err, "error injecting secret")
		}

		uid, err := strconv.Atoi(s.File.UID)
		if err != nil {
			return err
		}
		gid, err := strconv.Atoi(s.File.GID)
		if err != nil {
			return err
		}

		if err := os.Chown(fPath, ruid+uid, rgid+gid); err != nil {
			return errors.Wrap(err, "error setting ownership for secret")
		}
		if err := os.Chmod(fPath, s.File.Mode); err != nil {
			return errors.Wrap(err, "error setting file mode for secret")
		}
	}

	for _, configRef := range ctr.ConfigReferences {
		// TODO (ehazlett): use type switch when more are supported
		if configRef.File == nil {
			// Runtime configs are not mounted into the container, but they're
			// a valid type of config so we should not error when we encounter
			// one.
			if configRef.Runtime == nil {
				log.G(context.TODO()).Error("config target type is not a file or runtime target")
			}
			// However, in any case, this isn't a file config, so we have no
			// further work to do
			continue
		}

		fPath, err := ctr.ConfigFilePath(*configRef)
		if err != nil {
			return errors.Wrap(err, "error getting config file path for container")
		}
		if err := user.MkdirAllAndChown(filepath.Dir(fPath), 0o700, ruid, rgid); err != nil {
			return errors.Wrap(err, "error creating config mount path")
		}

		log.G(context.TODO()).WithFields(log.Fields{
			"name": configRef.File.Name,
			"path": fPath,
		}).Debug("injecting config")
		config, err := ctr.DependencyStore.Configs().Get(configRef.ConfigID)
		if err != nil {
			return errors.Wrap(err, "unable to get config from config store")
		}
		if err := os.WriteFile(fPath, config.Spec.Data, configRef.File.Mode); err != nil {
			return errors.Wrap(err, "error injecting config")
		}

		uid, err := strconv.Atoi(configRef.File.UID)
		if err != nil {
			return err
		}
		gid, err := strconv.Atoi(configRef.File.GID)
		if err != nil {
			return err
		}

		if err := os.Chown(fPath, ruid+uid, rgid+gid); err != nil {
			return errors.Wrap(err, "error setting ownership for config")
		}
		if err := os.Chmod(fPath, configRef.File.Mode); err != nil {
			return errors.Wrap(err, "error setting file mode for config")
		}
	}

	return daemon.remountSecretDir(ctr)
}

// createSecretsDir is used to create a dir suitable for storing container secrets.
// In practice this is using a tmpfs mount and is used for both "configs" and "secrets"
func (daemon *Daemon) createSecretsDir(ctr *container.Container) error {
	// retrieve possible remapped range start for root UID, GID
	uid, gid := daemon.idMapping.RootPair()
	dir, err := ctr.SecretMountPath()
	if err != nil {
		return errors.Wrap(err, "error getting container secrets dir")
	}

	// create tmpfs
	if err := user.MkdirAllAndChown(dir, 0o700, uid, gid); err != nil {
		return errors.Wrap(err, "error creating secret local mount path")
	}

	tmpfsOwnership := fmt.Sprintf("uid=%d,gid=%d", uid, gid)
	if err := mount.Mount("tmpfs", dir, "tmpfs", "nodev,nosuid,noexec,"+tmpfsOwnership); err != nil {
		return errors.Wrap(err, "unable to setup secret mount")
	}
	return nil
}

func (daemon *Daemon) remountSecretDir(ctr *container.Container) error {
	dir, err := ctr.SecretMountPath()
	if err != nil {
		return errors.Wrap(err, "error getting container secrets path")
	}
	if err := label.Relabel(dir, ctr.MountLabel, false); err != nil {
		log.G(context.TODO()).WithError(err).WithField("dir", dir).Warn("Error while attempting to set selinux label")
	}
	uid, gid := daemon.idMapping.RootPair()
	tmpfsOwnership := fmt.Sprintf("uid=%d,gid=%d", uid, gid)

	// remount secrets ro
	if err := mount.Mount("tmpfs", dir, "tmpfs", "remount,ro,"+tmpfsOwnership); err != nil {
		return errors.Wrap(err, "unable to remount dir as readonly")
	}

	return nil
}

func (daemon *Daemon) cleanupSecretDir(ctr *container.Container) {
	dir, err := ctr.SecretMountPath()
	if err != nil {
		log.G(context.TODO()).WithError(err).WithField("container", ctr.ID).Warn("error getting secrets mount path for container")
	}
	if err := mount.RecursiveUnmount(dir); err != nil {
		log.G(context.TODO()).WithField("dir", dir).WithError(err).Warn("Error while attempting to unmount dir, this may prevent removal of container.")
	}
	if err := os.RemoveAll(dir); err != nil {
		log.G(context.TODO()).WithField("dir", dir).WithError(err).Error("Error removing dir.")
	}
}

func killProcessDirectly(ctr *container.Container) error {
	pid := ctr.GetPID()
	if pid == 0 {
		// Ensure that we don't kill ourselves
		return nil
	}

	if err := unix.Kill(pid, syscall.SIGKILL); err != nil {
		if !errors.Is(err, unix.ESRCH) {
			return errdefs.System(err)
		}
		err = errNoSuchProcess{pid, syscall.SIGKILL}
		log.G(context.TODO()).WithFields(log.Fields{
			"error":     err,
			"container": ctr.ID,
			"pid":       pid,
		}).Debug("no such process")
		return err
	}

	// In case there were some exceptions(e.g., state of zombie and D)
	if process.Alive(pid) {
		// Since we can not kill a zombie pid, add zombie check here
		isZombie, err := process.Zombie(pid)
		if err != nil {
			log.G(context.TODO()).WithFields(log.Fields{
				"error":     err,
				"container": ctr.ID,
				"pid":       pid,
			}).Warn("Container state is invalid")
			return err
		}
		if isZombie {
			return errdefs.System(errors.Errorf("container %s PID %d is zombie and can not be killed. Use the --init option when creating containers to run an init inside the container that forwards signals and reaps processes", stringid.TruncateID(ctr.ID), pid))
		}
	}
	return nil
}

// TODO(aker): remove when we make the default bridge network behave like any other network
func enableIPOnPredefinedNetwork() bool {
	return false
}

// serviceDiscoveryOnDefaultNetwork indicates if service discovery is supported on the default network
// TODO(aker): remove when we make the default bridge network behave like any other network
func serviceDiscoveryOnDefaultNetwork() bool {
	return false
}

func buildSandboxPlatformOptions(ctr *container.Container, cfg *config.Config, sboxOptions *[]libnetwork.SandboxOption) error {
	var err error
	var originResolvConfPath string

	// Set the correct paths for /etc/hosts and /etc/resolv.conf, based on the
	// networking-mode of the container. Note that containers with "container"
	// networking are already handled in "initializeNetworking()" before we reach
	// this function, so do not have to be accounted for here.
	switch {
	case ctr.HostConfig.NetworkMode.IsHost():
		// In host-mode networking, the container does not have its own networking
		// namespace, so both `/etc/hosts` and `/etc/resolv.conf` should be the same
		// as on the host itself. The container gets a copy of these files.
		*sboxOptions = append(
			*sboxOptions,
			libnetwork.OptionOriginHostsPath("/etc/hosts"),
		)
		originResolvConfPath = "/etc/resolv.conf"
	case ctr.HostConfig.NetworkMode.IsUserDefined():
		// The container uses a user-defined network. We use the embedded DNS
		// server for container name resolution and to act as a DNS forwarder
		// for external DNS resolution.
		// We parse the DNS server(s) that are defined in /etc/resolv.conf on
		// the host, which may be a local DNS server (for example, if DNSMasq or
		// systemd-resolvd are in use). The embedded DNS server forwards DNS
		// resolution to the DNS server configured on the host, which in itself
		// may act as a forwarder for external DNS servers.
		// If systemd-resolvd is used, the "upstream" DNS servers can be found in
		// /run/systemd/resolve/resolv.conf. We do not query those DNS servers
		// directly, as they can be dynamically reconfigured.
		originResolvConfPath = "/etc/resolv.conf"
	default:
		// For other situations, such as the default bridge network, container
		// discovery / name resolution is handled through /etc/hosts, and no
		// embedded DNS server is available. Without the embedded DNS, we
		// cannot use local DNS servers on the host (for example, if DNSMasq or
		// systemd-resolvd is used). If systemd-resolvd is used, we try to
		// determine the external DNS servers that are used on the host.
		// This situation is not ideal, because DNS servers configured in the
		// container are not updated after the container is created, but the
		// DNS servers on the host can be dynamically updated.
		//
		// Copy the host's resolv.conf for the container (/run/systemd/resolve/resolv.conf or /etc/resolv.conf)
		originResolvConfPath = cfg.GetResolvConf()
	}

	// Allow tests to point at their own resolv.conf file.
	if envPath := os.Getenv("DOCKER_TEST_RESOLV_CONF_PATH"); envPath != "" {
		log.G(context.TODO()).Infof("Using OriginResolvConfPath from env: %s", envPath)
		originResolvConfPath = envPath
	}
	*sboxOptions = append(*sboxOptions, libnetwork.OptionOriginResolvConfPath(originResolvConfPath))

	ctr.HostsPath, err = ctr.GetRootResourcePath("hosts")
	if err != nil {
		return err
	}
	*sboxOptions = append(*sboxOptions, libnetwork.OptionHostsPath(ctr.HostsPath))

	ctr.ResolvConfPath, err = ctr.GetRootResourcePath("resolv.conf")
	if err != nil {
		return err
	}
	*sboxOptions = append(*sboxOptions, libnetwork.OptionResolvConfPath(ctr.ResolvConfPath))

	return nil
}

func (daemon *Daemon) initializeNetworkingPaths(ctr *container.Container, nc *container.Container) error {
	ctr.HostnamePath = nc.HostnamePath
	ctr.HostsPath = nc.HostsPath
	ctr.ResolvConfPath = nc.ResolvConfPath
	return nil
}

func (daemon *Daemon) setupContainerMountsRoot(ctr *container.Container) error {
	// get the root mount path so we can make it unbindable
	p, err := ctr.MountsResourcePath("")
	if err != nil {
		return err
	}
	_, gid := daemon.IdentityMapping().RootPair()
	return user.MkdirAllAndChown(p, 0o710, os.Getuid(), gid)
}
