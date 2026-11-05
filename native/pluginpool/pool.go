package pluginpool

import (
	"errors"
	"os"
	"path/filepath"
)

// Logger receives plugin output and supervision events.
type Logger interface {
	Info(args ...any)
	Warn(args ...any)
	Error(args ...any)
}

// Protector exempts one plugin socket from the VPN tunnel.
type Protector func(fd int) error

var errClosed = errors.New("plugin host closed")

// ReadEnvelope returns the envelope beside the first config file that has
// one. The CLI's working directory is not the app's (asteriskd runs the core
// from "/"), so only config locations are searched.
func ReadEnvelope(configPaths []string) (*Options, error) {
	for _, path := range configPaths {
		content, err := os.ReadFile(filepath.Join(filepath.Dir(path), Envelope))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return Parse(string(content))
	}
	return nil, nil
}
