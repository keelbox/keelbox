package modules

import (
	"context"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
)

// CloudInitKeepFile is the cloud-init configuration keelbox keeps on a box
// that runs on AWS (docs/adr/0071-resize-park-resume.md). keelbox resize
// and keelbox resume create a new Lightsail instance from a snapshot of
// the box's disk. cloud-init takes that for a new machine and would delete
// the SSH host keys and make new ones, which the laptop has not pinned. On
// the other clouds the new server's user data tells it to keep them
// (provider.ResumeUserData); Lightsail's user data is a shell script that
// runs after cloud-init has done it, so the disk has to say so itself.
const CloudInitKeepFile = "/etc/cloud/cloud.cfg.d/99-keelbox.cfg"

// cloudIDFile is where cloud-init records which cloud it found the
// machine on: "aws" on EC2 and Lightsail.
const cloudIDFile = "/run/cloud-init/cloud-id"

const cloudInitKeepConfig = `# Managed by keelbox. cloud-init reads this when the disk boots on a new
# instance, as it does after keelbox resize and keelbox resume on AWS: keep
# the SSH host keys, the hostname and the accounts that are on the disk.
ssh_deletekeys: false
preserve_hostname: true
users: []
`

// onAWS reports whether cloud-init found the machine on AWS. A machine
// without cloud-init has no such file.
func onAWS(e *engine.Env) bool {
	data, err := e.X.ReadFile(cloudIDFile)
	return err == nil && strings.TrimSpace(string(data)) == "aws"
}

// cloudInitKeepStep keeps CloudInitKeepFile on a machine cloud-init
// identifies as AWS, and does nothing anywhere else. The box's config
// cannot decide it: on the box its target is always local.
func cloudInitKeepStep() engine.Step {
	step := engine.FileStep("cloud-init-keeps-host-keys", engine.File{Path: CloudInitKeepFile, Content: []byte(cloudInitKeepConfig), Mode: 0o644})
	check, apply := step.Check, step.Apply
	step.Check = func(ctx context.Context, e *engine.Env) (engine.Status, error) {
		if !onAWS(e) {
			return engine.Converged(), nil
		}
		return check(ctx, e)
	}
	step.Apply = func(ctx context.Context, e *engine.Env) error {
		if !onAWS(e) {
			return nil
		}
		return apply(ctx, e)
	}
	return step
}
