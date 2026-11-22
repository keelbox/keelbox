// Package backup runs restic against S3-compatible storage
// (docs/adr/0020-backups.md).
//
// restic runs as root, because only root can read the repository password
// and the bucket credentials. It never reads a user's files itself: each
// backup path is archived by tar running as the account that owns it, and
// restic stores that stream (restic backup --stdin-from-command). A restore
// streams the archive back (restic dump) into tar running as the same
// account. So a symlink or a race in an agent's home gets the agent nothing
// it could not already read or write, which is ADR 0015's rule applied to
// backups.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// Paths and units on the box.
const (
	// SecretDir holds the installed credentials, root only.
	SecretDir       = "/etc/keelbox/backup" //nolint:gosec // G101: a directory, not a credential
	CredentialsPath = SecretDir + "/s3.credentials"
	PasswordPath    = SecretDir + "/restic.password"
	StateDir        = "/var/lib/keelbox/backup"
	StatusPath      = StateDir + "/status.json"
	CacheDir        = "/var/cache/keelbox/restic"
	RestoreDir      = "/var/lib/keelbox/restore"
	ResticBin       = "/usr/local/bin/restic"
	Unit            = "keelbox-backup.service"
	Timer           = "keelbox-backup.timer"
	// Tag marks every snapshot keelbox makes; PathTag prefixes the source
	// path tag.
	Tag     = "keelbox"
	PathTag = "keelbox-path="
	// RestoreSubdir is where a restore lands in an account's home by
	// default. It is in the default exclusions.
	RestoreSubdir = "keelbox-restore"
)

// Source is one backup path and the account that reads and restores it.
type Source struct {
	Path    string
	Account string // "root", or the home's owner
	Label   string // home-agent, from the path
}

// File is the name of the archive inside the snapshot.
func (s Source) File() string { return "/" + s.Label + ".tar" }

// Label turns a path into a name for its archive.
func Label(p string) string {
	l := strings.ReplaceAll(strings.Trim(p, "/"), "/", "-")
	if l == "" {
		return "root"
	}
	return l
}

// Sources resolves backup.paths (default: the agent's home). A path in the
// admin's or the agent's home is read and restored as that account; any
// other path as root.
func Sources(e *engine.Env) []Source {
	agent, admin := e.AgentUser(), e.AdminUser()
	paths := e.Cfg.Backup.Paths
	if len(paths) == 0 {
		paths = []string{agent.Home}
	}
	var out []Source
	for _, p := range paths {
		out = append(out, Source{Path: p, Account: accountFor(p, agent, admin), Label: Label(p)})
	}
	return out
}

func accountFor(p string, users ...system.User) string {
	for _, u := range users {
		if u.Home != "" && u.Home != "/" && (p == u.Home || strings.HasPrefix(p, u.Home+"/")) {
			return u.Name
		}
	}
	return "root"
}

// Env is restic's environment: every value a path, never a secret. Keys a
// caller's environment could set to override the files are emptied.
func Env(e *engine.Env) []string {
	return []string{
		"RESTIC_REPOSITORY=" + e.Cfg.Backup.Repository,
		"RESTIC_PASSWORD_FILE=" + PasswordPath,
		"AWS_SHARED_CREDENTIALS_FILE=" + CredentialsPath,
		"AWS_PROFILE=default",
		"RESTIC_CACHE_DIR=" + CacheDir,
		"RESTIC_PASSWORD=", "RESTIC_PASSWORD_COMMAND=", "RESTIC_REPOSITORY_FILE=",
		"AWS_ACCESS_KEY_ID=", "AWS_SECRET_ACCESS_KEY=", "AWS_SESSION_TOKEN=",
		"MINIO_ACCESS_KEY=", "MINIO_SECRET_KEY=", "MINIO_ROOT_USER=", "MINIO_ROOT_PASSWORD=",
	}
}

func restic(e *engine.Env, args ...string) system.Cmd {
	c := system.C(ResticBin, args...)
	c.Env = Env(e)
	return c
}

// cleanEnv starts a reader or writer with nothing of restic's environment.
var cleanEnv = []string{"env", "-i", "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}

// asAccount prefixes a command so it runs as account, or as root.
func asAccount(account string, cmd ...string) []string {
	base := append([]string(nil), cleanEnv...)
	if account != "root" {
		base = append([]string{"/usr/sbin/runuser", "-u", account, "--"}, base...)
	}
	return append(base, cmd...)
}

// tarCreate archives the current directory. Exit 1 ("some files changed
// while being read") is normal on a live home and still a usable archive.
const tarCreate = `cd "$1" || exit 2
shift
tar --create --file=- --one-file-system --ignore-failed-read --exclude-caches \
  --warning=no-file-changed --warning=no-file-removed --warning=no-file-shrank --warning=no-file-ignored "$@" .
rc=$?
[ "$rc" -le 1 ] || exit "$rc"`

// ReaderCommand is the command restic runs to read a source.
func ReaderCommand(s Source, exclude []string) []string {
	args := []string{"/bin/sh", "-c", tarCreate, "sh", s.Path}
	for _, x := range exclude {
		args = append(args, "--exclude="+x)
	}
	return asAccount(s.Account, args...)
}

// Status is the last run, kept root-only for doctor and status.
type Status struct {
	LastAttempt time.Time `json:"last_attempt"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	// Repository is the repository known to exist, so later runs skip
	// the init attempt.
	Repository string   `json:"repository,omitempty"`
	Error      string   `json:"error,omitempty"`
	Snapshots  []string `json:"snapshots,omitempty"`
}

// ReadStatus reads the status file.
func ReadStatus(e *engine.Env) (Status, bool) {
	data, err := e.X.ReadFile(StatusPath)
	if err != nil || len(data) == 0 {
		return Status{}, false
	}
	var s Status
	return s, json.Unmarshal(data, &s) == nil
}

func writeStatus(e *engine.Env, s Status) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return e.X.WriteFile(StatusPath, append(b, '\n'), 0o600, "root", "root")
}

// exitCode returns a command's exit code, or -1.
func exitCode(err error) int {
	var ee *system.ExitError
	if errors.As(err, &ee) {
		return ee.Result.ExitCode
	}
	return -1
}

// ensureRepo initialises the repository the first time. It tries init
// rather than probing: on a missing bucket restic's probes retry for about
// 15 minutes, while init creates the bucket, and on an existing repository
// fails at once with "config file already exists".
func ensureRepo(ctx context.Context, e *engine.Env, out io.Writer, known string) error {
	repo := e.Cfg.Backup.Repository
	if known == repo {
		return nil
	}
	_, err := e.X.Run(ctx, restic(e, "init", "--quiet"))
	if err == nil {
		fmt.Fprintf(out, "initialised the restic repository %s\n", repo)
		return nil
	}
	var ee *system.ExitError
	if errors.As(err, &ee) && strings.Contains(ee.Result.Stderr, "config file already exists") {
		return nil
	}
	return fmt.Errorf("open or initialise the repository %s: %w", repo, err)
}

type summary struct {
	MessageType   string  `json:"message_type"`
	SnapshotID    string  `json:"snapshot_id"`
	FilesNew      int     `json:"files_new"`
	DataAdded     int64   `json:"data_added"`
	TotalBytes    int64   `json:"total_bytes_processed"`
	TotalDuration float64 `json:"total_duration"`
}

func parseSummary(out string) (summary, bool) {
	for _, l := range strings.Split(out, "\n") {
		var s summary
		if json.Unmarshal([]byte(l), &s) == nil && s.MessageType == "summary" {
			return s, true
		}
	}
	return summary{}, false
}

// Run backs up every source, then applies the retention policy and prunes.
// The status file records the outcome either way.
func Run(ctx context.Context, e *engine.Env, out io.Writer) error {
	prev, _ := ReadStatus(e)
	st := Status{LastAttempt: time.Now().UTC(), LastSuccess: prev.LastSuccess, Repository: prev.Repository}
	err := run(ctx, e, out, &st)
	if err != nil {
		st.Error = err.Error()
	} else {
		st.LastSuccess = st.LastAttempt
	}
	if werr := writeStatus(e, st); werr != nil && err == nil {
		err = werr
	}
	return err
}

func run(ctx context.Context, e *engine.Env, out io.Writer, st *Status) error {
	if err := ensureRepo(ctx, e, out, st.Repository); err != nil {
		st.Repository = ""
		return err
	}
	st.Repository = e.Cfg.Backup.Repository
	for _, s := range Sources(e) {
		if err := checkSource(e, s); err != nil {
			return err
		}
		args := []string{"backup", "--json", "--host", e.Cfg.Box, "--tag", Tag, "--tag", PathTag + s.Path,
			"--stdin-filename", strings.TrimPrefix(s.File(), "/"), "--stdin-from-command", "--"}
		args = append(args, ReaderCommand(s, e.Cfg.BackupExclude())...)
		r, err := e.X.Run(ctx, restic(e, args...))
		if err != nil {
			return fmt.Errorf("back up %s: %w", s.Path, err)
		}
		sum, _ := parseSummary(r.Stdout)
		st.Snapshots = append(st.Snapshots, sum.SnapshotID)
		fmt.Fprintf(out, "backed up %s as %s: snapshot %s, %s read, %s new in the repository\n",
			s.Path, s.Account, short(sum.SnapshotID), size(sum.TotalBytes), size(sum.DataAdded))
	}
	d, w, m := e.Cfg.KeepPolicy()
	args := []string{"forget", "--quiet", "--host", e.Cfg.Box, "--tag", Tag, "--group-by", "host,tags", "--prune"}
	for _, k := range []struct {
		flag string
		n    int
	}{{"--keep-daily", d}, {"--keep-weekly", w}, {"--keep-monthly", m}} {
		if k.n > 0 {
			args = append(args, k.flag, strconv.Itoa(k.n))
		}
	}
	if _, err := e.X.Run(ctx, restic(e, args...)); err != nil {
		return fmt.Errorf("apply the retention policy: %w", err)
	}
	fmt.Fprintf(out, "kept %d daily, %d weekly and %d monthly snapshots per path; pruned the rest\n", d, w, m)
	return nil
}

// checkSource refuses a path that does not exist for its account, and a
// root path another account could redirect.
func checkSource(e *engine.Env, s Source) error {
	st, err := e.FS(s.Account).Stat(s.Path)
	if err != nil {
		return fmt.Errorf("backup path %s (read as %s): %w", s.Path, s.Account, err)
	}
	if !st.Exists || !st.IsDir {
		return fmt.Errorf("backup path %s is not a directory %s can read", s.Path, s.Account)
	}
	return nil
}

// Snapshot is one restic snapshot keelbox made.
type Snapshot struct {
	ID      string    `json:"id"`
	ShortID string    `json:"short_id"`
	Time    time.Time `json:"time"`
	Tags    []string  `json:"tags"`
	Summary *struct {
		TotalBytes int64 `json:"total_bytes_processed"`
	} `json:"summary"`
}

// Path is the source path recorded in the snapshot's tags.
func (s Snapshot) Path() string {
	for _, t := range s.Tags {
		if strings.HasPrefix(t, PathTag) {
			return strings.TrimPrefix(t, PathTag)
		}
	}
	return ""
}

// Snapshots lists this box's snapshots, oldest first.
func Snapshots(ctx context.Context, e *engine.Env) ([]Snapshot, error) {
	r, err := e.X.Run(ctx, restic(e, "snapshots", "--json", "--host", e.Cfg.Box, "--tag", Tag))
	if err != nil {
		if exitCode(err) == 10 {
			return nil, errors.New("the repository does not exist yet: run keelbox backup first")
		}
		return nil, err
	}
	var ss []Snapshot
	if err := json.Unmarshal([]byte(r.Stdout), &ss); err != nil {
		return nil, fmt.Errorf("read restic snapshots: %w", err)
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].Time.Before(ss[j].Time) })
	return ss, nil
}

// List prints the snapshots.
func List(ctx context.Context, e *engine.Env, out io.Writer) error {
	ss, err := Snapshots(ctx, e)
	if err != nil {
		return err
	}
	if len(ss) == 0 {
		fmt.Fprintln(out, "No snapshots yet. Run keelbox backup.")
		return nil
	}
	fmt.Fprintf(out, "%-10s %-20s %-10s %s\n", "SNAPSHOT", "TIME (UTC)", "SIZE", "PATH")
	for _, s := range ss {
		sz := "-"
		if s.Summary != nil {
			sz = size(s.Summary.TotalBytes)
		}
		fmt.Fprintf(out, "%-10s %-20s %-10s %s\n", short(s.ID), s.Time.UTC().Format("2006-01-02 15:04:05"), sz, s.Path())
	}
	return nil
}

// RestoreOptions choose what to restore and where.
type RestoreOptions struct {
	Snapshot  string // an ID or prefix, "latest", or "" for the latest of each path
	Target    string // a directory to restore into; "" for the default
	Overwrite bool   // restore over the original path
}

// Restore extracts snapshots as the accounts that own their paths. By
// default each lands in a new directory: <home>/keelbox-restore/<snapshot>
// for a path in a home, /var/lib/keelbox/restore/<snapshot> otherwise.
// Overwrite restores over the original path, replacing files the snapshot
// has and keeping files it does not.
func Restore(ctx context.Context, e *engine.Env, o RestoreOptions, out io.Writer) error {
	if o.Overwrite && o.Target != "" {
		return errors.New("--overwrite restores over the original path; leave out --target")
	}
	if o.Target != "" && (!path.IsAbs(o.Target) || path.Clean(o.Target) != o.Target) {
		return fmt.Errorf("--target %s: give a clean absolute path", o.Target)
	}
	ss, err := Snapshots(ctx, e)
	if err != nil {
		return err
	}
	chosen, err := choose(e, ss, o.Snapshot)
	if err != nil {
		return err
	}
	agent, admin := e.AgentUser(), e.AdminUser()
	for _, s := range chosen {
		src := Source{Path: s.Path(), Account: accountFor(s.Path(), agent, admin), Label: Label(s.Path())}
		target := o.Target
		switch {
		case o.Overwrite:
			target = src.Path
		case target != "" && len(chosen) > 1:
			target = path.Join(target, src.Label)
		case target == "" && src.Account == "root":
			target = path.Join(RestoreDir, short(s.ID))
		case target == "":
			home := agent.Home
			if src.Account == admin.Name {
				home = admin.Home
			}
			target = path.Join(home, RestoreSubdir, short(s.ID))
		}
		if err := prepareTarget(ctx, e, src.Account, target, o.Overwrite); err != nil {
			return err
		}
		if err := extract(ctx, e, s, src, target); err != nil {
			return err
		}
		verb := "into"
		if o.Overwrite {
			verb = "over"
		}
		fmt.Fprintf(out, "restored %s from snapshot %s (%s) %s %s, as %s\n",
			src.Path, short(s.ID), s.Time.UTC().Format("2006-01-02 15:04 MST"), verb, target, src.Account)
	}
	return nil
}

// choose picks one snapshot by ID prefix or the latest one per path.
func choose(e *engine.Env, ss []Snapshot, want string) ([]Snapshot, error) {
	if len(ss) == 0 {
		return nil, errors.New("no snapshots yet: run keelbox backup first")
	}
	latest := map[string]Snapshot{}
	for _, s := range ss {
		if s.Path() != "" {
			latest[s.Path()] = s // ss is oldest first
		}
	}
	switch want {
	case "", "latest":
		var out []Snapshot
		for _, src := range Sources(e) {
			if s, ok := latest[src.Path]; ok {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil, errors.New("no snapshot of the configured backup paths; name one from keelbox backup --list")
		}
		return out, nil
	}
	var match []Snapshot
	for _, s := range ss {
		if strings.HasPrefix(s.ID, want) {
			match = append(match, s)
		}
	}
	switch {
	case len(match) == 0:
		return nil, fmt.Errorf("no snapshot %s of this box (keelbox backup --list)", want)
	case len(match) > 1:
		return nil, fmt.Errorf("snapshot %s is ambiguous; give more of its ID", want)
	case match[0].Path() == "":
		return nil, fmt.Errorf("snapshot %s has no keelbox path tag", want)
	}
	return match, nil
}

// prepareTarget makes the target directory as account. Without overwrite it
// must be new or empty, so a restore never mixes with what is there.
func prepareTarget(ctx context.Context, e *engine.Env, account, target string, overwrite bool) error {
	fs := e.FS(account)
	st, err := fs.Stat(target)
	if err != nil {
		return fmt.Errorf("restore target %s (as %s): %w", target, account, err)
	}
	if st.Exists && !st.IsDir {
		return fmt.Errorf("restore target %s exists and is not a directory", target)
	}
	if st.Exists && !overwrite {
		c := system.C("find", target, "-mindepth", "1", "-maxdepth", "1", "-print", "-quit")
		if account != "root" {
			c.User = account
		}
		first, err := system.Output(ctx, e.X, c)
		if err != nil {
			return fmt.Errorf("restore target %s (as %s): %w", target, account, err)
		}
		if first != "" {
			return fmt.Errorf("restore target %s is not empty; pick a new directory, or pass --overwrite to restore over the original path", target)
		}
	}
	if !st.Exists {
		if err := fs.MkdirAll(target, 0o700, account, account); err != nil {
			return fmt.Errorf("create restore target %s as %s: %w", target, account, err)
		}
	}
	return nil
}

// restoreScript streams the archive from restic into the writer command.
const restoreScript = `set -euo pipefail
snap=$1 file=$2
shift 2
` + ResticBin + ` dump --quiet -- "$snap" "$file" | "$@"`

func extract(ctx context.Context, e *engine.Env, s Snapshot, src Source, target string) error {
	args := []string{"-c", restoreScript, "bash", s.ID, src.File()}
	args = append(args, asAccount(src.Account, "tar", "--extract", "--file=-", "--directory="+target)...)
	c := system.C("/bin/bash", args...)
	c.Env = Env(e)
	if _, err := e.X.Run(ctx, c); err != nil {
		return fmt.Errorf("restore %s into %s: %w", src.Path, target, err)
	}
	return nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
