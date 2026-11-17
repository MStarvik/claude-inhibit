# claude-inhibit

Keeps the machine awake while a Claude Code agent is working, and lets it
sleep again as soon as no agent is.

It holds a systemd-logind `idle` inhibitor lock whenever at least one Claude
Code session is working, and releases it when none are.

Only `idle` is taken by default: it stops logind's automatic `IdleAction`
(e.g. `IdleAction=suspend` in `logind.conf`) and idle daemons that honour
logind inhibitors, but you can still suspend deliberately. Adding `sleep`
would block deliberate suspends too (`systemctl suspend` fails as inhibited).

Idle daemons that ignore logind inhibitors aren't covered: swayidle, for
example, only honours Wayland idle-inhibit, so with swayidle-driven suspend
this has no effect. (hypridle honours them by default; GNOME uses its own
session inhibitors.)

## How it works

Claude Code writes a status file per running session to
`~/.claude/sessions/<pid>.json`. A session counts as **working** when its
process is still alive and either:

- its `status` is anything but `idle` (seen: `busy` while the model works,
  `shell` while a Bash command runs), or
- it is `idle` but still has a Bash-tool child process: a background task or
  Monitor the agent is waiting on. These are recognised by the
  `~/.claude/shell-snapshots/` script every Bash-tool command sources, so MCP
  servers and other long-lived children don't count. Disable with
  `-background=false`.

Interrupting an agent needs no special handling: its status returns to `idle`
and the lock is released on the next scan. Hooks can't do this reliably,
because no hook fires on interrupt.

The directory is watched with inotify (reacting within ~200 ms), with a rescan
every `-interval` (default 10s) to catch exited processes and finished
background tasks. Work *starting* always rewrites a session file, so the
interval only affects how quickly the lock is released, never whether it is
taken. Stale files are ignored: the pid must be alive *and* its
start time must match the file's `procStart`, so a reused pid doesn't count.

The lock is taken over D-Bus (`org.freedesktop.login1.Manager.Inhibit`), which
returns a file descriptor that *is* the lock. If claude-inhibit is killed, even
with SIGKILL, the kernel closes the fd and logind drops the lock, so it can
never get stuck on.

## Install

```sh
make enable          # build, install to ~/.local/bin, enable the user service
journalctl --user -u claude-inhibit -f
systemd-inhibit --list
```

Or without cloning: `go install github.com/MStarvik/claude-inhibit@latest`,
then install `claude-inhibit.service` into `~/.config/systemd/user/`.

`claude-inhibit -once` prints the sessions that currently count as working.

## License

[CC0 1.0](LICENSE): public domain dedication.

## Options

| flag          | default      | meaning                                                         |
|---------------|--------------|-----------------------------------------------------------------|
| `-what`       | `idle`       | locks to take, e.g. `idle:handle-lid-switch`                    |
| `-mode`       | `block`      | `block` or `delay`                                              |
| `-background` | `true`       | count idle sessions with a running background task as working   |
| `-interval`   | `10s`        | periodic rescan interval                                        |
| `-once`       |              | print working sessions and exit                                 |

Set flags with a drop-in: `systemctl --user edit claude-inhibit`, then
override `ExecStart=` (clear it with an empty `ExecStart=` line first).

## Caveat

`~/.claude/sessions/*.json` is undocumented and could change in any Claude
Code release. If it does, claude-inhibit fails safe: it stops taking the lock
rather than holding it forever.
