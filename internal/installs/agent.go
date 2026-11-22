package installs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

// The agent's side: keelbox install, run as the agent, with only its own
// file access. Root trusts none of it.

// WriteRequest replaces the request file at path with r, atomically, so
// root never reads half of one. The directory and the file are the
// agent's own (0700, 0600).
func WriteRequest(path string, r Request) error {
	r.V = 1
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".install-request-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// LockRequests takes the agent's own lock beside its request file, so two
// keelbox install commands of the agent's do not replace each other's
// request before root has read it. It is a courtesy between the agent's
// own commands: root does not rely on it.
func LockRequests(dir string) (unlock func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "install.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// LoadMode reads the box's mode file as whoever runs keelbox: it is
// world-readable.
func LoadMode(path string) (Mode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Mode{}, err
	}
	return ParseMode(data)
}

// LoadState reads root's record of the requests as whoever runs keelbox.
func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	return ParseState(data)
}
