# Security policy

Bao Policy Editor (BPE) edits OpenBao ACL policies, and in remote mode it holds
an OpenBao token and writes to a live server. That makes a security report here
worth taking seriously, and it also makes *how* you report one matter.

## Reporting a vulnerability

**Report privately, through GitHub's private vulnerability reporting**, at
<https://github.com/kevinpinscoe/bao-policy-editor/security/advisories/new>.

Do not open a public issue, a pull request, or a discussion for a suspected
vulnerability. A public report is readable by everyone from the moment it is
filed, including by anyone who would use it.

If private reporting is unavailable to you for any reason, say so in a public
issue **without the details** — "I have a security report and cannot use private
reporting" is enough — and you will be given another route.

### What to expect

This is a personal open-source project maintained by one person, not a funded
product with an on-call rota. There is no guaranteed response time. A report
will be acknowledged when it is seen, and you will be told whether it is
accepted, already known, or not considered a vulnerability.

## Do not include secrets in a report

This is the part that goes wrong most often, so it is stated plainly.

**Never put any of the following in a report, an issue, a pull request, a log
excerpt, a screenshot, or an attached file:**

- An OpenBao token, of any kind — root, service, batch, or a wrapped token.
- The contents of `BAO_TOKEN`, `VAULT_TOKEN`, or any file they were read from.
- Client certificates or private keys.
- Any secret *value* read out of OpenBao.
- A policy that names real internal paths, mount names, team names, hostnames,
  or account identifiers, where those are themselves sensitive.
- An unredacted terminal recording or screenshot of a session against a real
  server.

A report almost never needs any of them. What it usually needs is the shape of
the problem: the version of BPE, the version of OpenBao, the sequence of actions,
and a **synthetic** policy that reproduces the behaviour. `testdata/policies/`
holds fixtures you can adapt.

**If you believe a token has already been exposed** — in a log you were about to
send, in a terminal recording, in a CI job — revoke it first
(`bao token revoke -self`, or revoke it as an operator) and then write the
report. Revoking costs nothing; a token that reaches an issue tracker is public.

## What BPE does with your credentials

Stated so you can judge for yourself what a vulnerability here would look like:

- BPE **reads** a token from the environment (`BAO_TOKEN`, or the `VAULT_TOKEN`
  fallback the official client supports). It never asks for one on screen, and
  the connect screen shows only whether a token is `configured` or
  `not configured` — never the value.
- A `--token` flag exists and takes precedence over the environment. **Prefer the
  environment variable.** A flag value is visible in the process list to other
  users on the machine and is written to your shell history; that is a property
  of command lines, not something BPE can fix from inside. No example in this
  repository puts a token on a command line, and none should.
- BPE **never writes a token** to disk, to a log, or to a file it creates.
- Errors are scrubbed before they are returned: any occurrence of the token in a
  rendered error message is replaced with `<redacted>`, as a backstop in case a
  dependency ever puts it there. This is covered by a test.
- BPE **does not read secret values**. It reads and writes ACL *policies*, and
  nothing under a secrets mount.
- TLS verification is **on by default**. It can be disabled explicitly, and when
  it is, BPE shows a persistent textual warning that does not depend on colour.
- BPE makes **no network connection at all** in offline mode. Editing, validating
  and simulating a local file constructs no client and contacts nothing.
- BPE sends **no telemetry**, ever.

See the Security behavior section of [README.md](README.md) for the detail behind
each of these.

## Supported versions

BPE is **experimental** and has not had a tagged release. There is no supported
version to backport to yet: fixes land on `main`. When the first release is
tagged, this section will say which versions receive fixes.

## Scope

In scope: anything that would expose a token or a secret, cause BPE to write a
policy the user did not confirm, silently lose policy content, defeat the
check-and-set conflict protection on a remote write, or disable TLS verification
without saying so.

Out of scope: the security of OpenBao itself (report that to the OpenBao
project), and the consequences of a policy a user deliberately wrote. BPE will
warn about a dangerous policy; it will not refuse to let you write one.
