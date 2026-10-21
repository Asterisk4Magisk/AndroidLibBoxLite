package systemconfig

import (
	"net/netip"
	"sync/atomic"
	"time"

	environment "github.com/sagernet/sing-box/common/androidcli"
	M "github.com/sagernet/sing/common/metadata"
)

// Android has no resolv.conf. Standalone ROOT publishes the physical network's
// DNS from its own observer instead; JNI keeps the platform transport.
var managed atomic.Pointer[Config]

// SetManagedServers publishes a new immutable configuration. The local
// transport rebuilds its server set on the new pointer, and DNS cache entries
// are keyed by the configuration signature.
func SetManagedServers(servers []netip.AddrPort) {
	config := &Config{
		Ndots:    1,
		Timeout:  5 * time.Second,
		Attempts: 2,
		Search:   defaultSearch(),
	}
	for _, server := range servers {
		config.Servers = append(config.Servers, M.SocksaddrFromNetIP(server))
	}
	managed.Store(config)
}

// CLI tool commands start no observer and retain the upstream source.
func managedConfiguration() *Config {
	if !environment.ManagedSystemDNS() {
		return nil
	}
	return managed.Load()
}
