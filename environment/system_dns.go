package environment

import "os"

// The DNS hot path consults this; a core never changes its effective UID.
var managedSystemDNS = IsCLI && os.Geteuid() == 0

// Only standalone ROOT owns a Java observer. JNI keeps the app's platform
// transport; non-root standalone invocations retain upstream resolv.conf.
func ManagedSystemDNS() bool {
	return managedSystemDNS
}
