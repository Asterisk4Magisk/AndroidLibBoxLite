//go:build !linux

package pluginpool

import "errors"

// Host is a stub: plugins rely on Linux process and socket semantics.
type Host struct{}

func NewHost(Logger, Protector, func(error)) *Host {
	return &Host{}
}

func (h *Host) Apply(options *Options) error {
	if options == nil {
		return nil
	}
	return errors.New("plugins require Linux or Android")
}

func (h *Host) Close() error {
	return nil
}
