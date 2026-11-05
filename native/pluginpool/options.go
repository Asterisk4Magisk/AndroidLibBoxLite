// Package pluginpool runs protocol plugins as supervised child processes.
//
// The application describes the processes in a JSON envelope, using the field
// names of Husi's plugin pool so existing plugin packages keep working. The
// core writes each plugin's files, executes it, restarts it after a crash and,
// in VPN mode, protects the sockets the plugin hands back over a Unix socket.
package pluginpool

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Envelope is the file name the CLI reads beside its first config file.
const Envelope = "plugin.json"

const (
	fileTokenPrefix = "${file:"
	fileTokenSuffix = "}"
)

// Options is the envelope describing every plugin of one service.
type Options struct {
	WorkingDir  string `json:"workingDir"`
	ProtectPath string `json:"protectPath"`
	VerifyPeer  bool   `json:"verifyPeer"`
	Plugins     []Spec `json:"plugins"`
}

// Spec describes one plugin process.
type Spec struct {
	Name        string            `json:"name"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Files       []File            `json:"files"`
}

// File is written into the working directory before the plugin starts and
// removed after it stops. ${file:Name} in the command or environment expands
// to its path.
type File struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Parse decodes and validates an envelope. Blank content or an envelope
// without plugins yields nil: the service runs without plugins.
func Parse(content string) (*Options, error) {
	if strings.TrimSpace(content) == "" {
		return nil, nil
	}
	var options Options
	if err := json.Unmarshal([]byte(content), &options); err != nil {
		return nil, fmt.Errorf("decode plugin envelope: %w", err)
	}
	if len(options.Plugins) == 0 {
		return nil, nil
	}
	if err := options.validate(); err != nil {
		return nil, err
	}
	return &options, nil
}

func (o *Options) validate() error {
	if !filepath.IsAbs(o.WorkingDir) {
		return fmt.Errorf("plugin working directory must be absolute: %q", o.WorkingDir)
	}
	if o.ProtectPath != "" && !filepath.IsAbs(o.ProtectPath) {
		return fmt.Errorf("plugin protect path must be absolute: %q", o.ProtectPath)
	}
	// Plugins share one directory, so a name collision would let one plugin's
	// cleanup delete another's file.
	names := make(map[string]bool)
	for index := range o.Plugins {
		spec := &o.Plugins[index]
		if spec.Name == "" {
			spec.Name = fmt.Sprint("plugin-", index)
		}
		// Never resolve the executable through PATH.
		if len(spec.Command) == 0 || !filepath.IsAbs(spec.Command[0]) {
			return fmt.Errorf("%s: command must start with an absolute path", spec.Name)
		}
		for _, file := range spec.Files {
			if err := validateFileName(file.Name); err != nil {
				return fmt.Errorf("%s: %w", spec.Name, err)
			}
			if names[file.Name] {
				return fmt.Errorf("%s: duplicate plugin file %q", spec.Name, file.Name)
			}
			names[file.Name] = true
		}
	}
	return nil
}

func validateFileName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid plugin file name %q", name)
	}
	return nil
}

// expand replaces ${file:Name} with the written path. An unknown name is an
// error so a misconfigured plugin fails its start instead of its first dial.
func expand(value, workingDir string, files map[string]bool) (string, error) {
	if !strings.Contains(value, fileTokenPrefix) {
		return value, nil
	}
	var builder strings.Builder
	for {
		before, rest, found := strings.Cut(value, fileTokenPrefix)
		builder.WriteString(before)
		if !found {
			return builder.String(), nil
		}
		name, after, closed := strings.Cut(rest, fileTokenSuffix)
		if !closed {
			return "", fmt.Errorf("unterminated plugin file reference")
		}
		if !files[name] {
			return "", fmt.Errorf("unknown plugin file %q", name)
		}
		builder.WriteString(filepath.Join(workingDir, name))
		value = after
	}
}
