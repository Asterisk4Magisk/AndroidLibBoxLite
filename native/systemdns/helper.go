//go:build android || linux

package systemdns

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	helperFilename = "system-dns-helper.dex"
	tempPattern    = ".system-dns-helper-*.dex"
)

// A core killed while replacing the dex leaves its temporary behind. Skip
// recent files in case another process is still writing one.
func removeStaleTemporaries(home string) {
	names, _ := filepath.Glob(filepath.Join(home, tempPattern))
	for _, name := range names {
		if info, err := os.Lstat(name); err == nil && info.Mode().IsRegular() &&
			time.Since(info.ModTime()) > time.Minute {
			_ = os.Remove(name)
		}
	}
}

// Retain the real path and ART's sibling oat cache across process exits.
// Android 8's class loader cannot open an already unlinked dex through procfs.
func ensureHelper(home string, payload []byte) (string, error) {
	home, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(home, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(home, helperFilename)
	match, err := helperMatches(path, payload)
	if err != nil || match {
		return path, err
	}
	removeStaleTemporaries(home)
	file, err := os.CreateTemp(home, tempPattern)
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(payload); err != nil {
		return "", err
	}
	if err = file.Chmod(0400); err != nil {
		return "", err
	}
	if err = file.Sync(); err != nil {
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

func helperMatches(path string, payload []byte) (bool, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == syscall.ENOENT || err == syscall.ELOOP {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("system DNS helper is not a regular file")
	}
	if info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || info.Size() != int64(len(payload)) {
		return false, nil
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(len(payload))+1))
	if err != nil || !bytes.Equal(content, payload) {
		return false, err
	}
	if info.Mode().Perm() != 0400 {
		if err = file.Chmod(0400); err != nil {
			return false, err
		}
	}
	return true, nil
}
