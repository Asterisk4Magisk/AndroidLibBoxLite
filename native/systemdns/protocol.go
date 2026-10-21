// Package systemdns consumes DNS configuration, never Android DNS queries.
package systemdns

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
)

const MaxFrame = 8192

func decodeSnapshot(frame []byte) ([]string, error) {
	var snapshot struct {
		Version int      `json:"version"`
		Servers []string `json:"servers"`
	}
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF || snapshot.Version != 1 || snapshot.Servers == nil || len(snapshot.Servers) > 32 {
		return nil, fmt.Errorf("invalid system DNS snapshot")
	}
	seen := make(map[string]bool)
	servers := make([]string, 0, len(snapshot.Servers))
	for _, server := range snapshot.Servers {
		endpoint, err := netip.ParseAddrPort(server)
		if err != nil || endpoint.Port() != 53 || endpoint.Addr().IsUnspecified() ||
			endpoint.Addr().IsLoopback() || endpoint.Addr().IsMulticast() {
			return nil, fmt.Errorf("invalid system DNS endpoint")
		}
		server = endpoint.String()
		if !seen[server] {
			seen[server] = true
			servers = append(servers, server)
		}
	}
	return servers, nil
}
