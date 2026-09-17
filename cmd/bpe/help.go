package main

const topLevelHelp = `bpe — a terminal interface for creating, editing, validating, and testing
OpenBao HCL ACL policies.

Usage:
  bpe                                     Start the interactive editor with an empty policy
  bpe <policy.hcl>                        Start the interactive editor, opening a policy file
  bpe validate <policy.hcl>               Validate a policy without starting the interactive editor
  bpe format <policy.hcl> [--check]       Format a policy file, or check whether it is formatted
  bpe test <policy.hcl> --path <path> --capability <capability>
                                           Simulate an effective-access check
  bpe help [command]                      Show this help, or help for one command
  bpe --help                              Show this help
  bpe --version                           Show version information

validate, format, and test are implemented — see bpe <command> --help for
each command's arguments and exit codes. The interactive editor
(bpe / bpe <policy.hcl>) is not implemented yet: it reports
"not implemented yet" and exits non-zero rather than starting a TUI — see
RUNBOOK.md for current limitations.

Global flags (accepted anywhere on the command line, before or after a
command; see Configuration in README.md for precedence):
  --address <url>              OpenBao server URL (BAO_ADDR / VAULT_ADDR)
  --token <token>               OpenBao authentication token (BAO_TOKEN / VAULT_TOKEN)
  --namespace <namespace>       OpenBao namespace (BAO_NAMESPACE / VAULT_NAMESPACE)
  --ca-cert <path>              CA certificate file (BAO_CACERT / VAULT_CACERT)
  --ca-path <path>              CA certificate directory (BAO_CAPATH / VAULT_CAPATH)
  --client-cert <path>          Client certificate file (BAO_CLIENT_CERT / VAULT_CLIENT_CERT)
  --client-key <path>           Client private key file (BAO_CLIENT_KEY / VAULT_CLIENT_KEY)
  --tls-server-name <name>      TLS server name override (BAO_TLS_SERVER_NAME / VAULT_TLS_SERVER_NAME)
  --skip-verify                 Disable TLS certificate verification (BAO_SKIP_VERIFY / VAULT_SKIP_VERIFY)

These are accepted and resolved now for the OpenBao client landing in a
later ticket; no command in this release contacts a server.
`

const validateHelp = `bpe validate <policy.hcl> — validate a policy without starting the
interactive editor.

Usage:
  bpe validate <policy.hcl>

Reads and parses the file, then runs semantic checks (unknown
capabilities, deny combined with other capabilities, duplicate path
blocks, suspicious wildcards, and more — see README.md's CLI Reference).
Every diagnostic is printed with its severity; warnings never fail
validation. Performs no write and never contacts OpenBao.

Exit codes: 0 no errors (warnings alone do not fail), 1 validation
failure, 3 the file could not be read. See README.md's Exit Codes.
`

const formatHelp = `bpe format <policy.hcl> [--check] — format a policy file.

Usage:
  bpe format <policy.hcl>
  bpe format <policy.hcl> --check

Canonicalizes the file's HCL formatting (spacing, indentation, alignment)
without altering its meaning. Gated on HCL syntax validity alone: a
syntactically valid file with unknown attributes, an invalid attribute
value, or contradictory permissions still formats — nothing recognized or
not is ever lost. A file that is already formatted is left untouched,
including its modification time.

--check reports whether the file would change without writing it or
altering it in any way; suitable for CI. It follows a symlink for that
read-only check. Writing in place never does: bpe format refuses to
format through a symlink, and refuses a hard-linked file where that is
detectable, rather than silently following or replacing either.

Writes are atomic (temp file + rename) and refuse to proceed if the file
changed on disk since it was read.

Exit codes: 0 formatted or already formatted, 1 a syntax error, or
(--check only) the file would be reformatted, 2 usage error, 3 an
operational failure (including a symlink/hard-link refusal), 4 the file
changed on disk since it was read. See README.md's Exit Codes.
`

const testHelp = `bpe test <policy.hcl> --path <path> --capability <capability> — simulate
an effective-access check against a local policy file.

Usage:
  bpe test <policy.hcl> --path <path> --capability <capability>

--path and --capability may appear before or after the policy file.

Reads and parses the file, then simulates an effective-access check
against internal/evaluator's OpenBao-compatible matcher and prints a
structured explanation of the decision. Performs no write and never
contacts OpenBao. See README.md's Evaluation limits for what it
deliberately does not attempt.

Exit codes: 0 allowed, 1 denied or the file has errors/unsupported
content it cannot trust, 2 an unknown --capability, 3 the result cannot
be reduced to a trustworthy decision, or the file could not be read. See
README.md's Exit Codes.
`

// helpText returns the help text for topic ("" for top-level help, or one
// of "validate"/"format"/"test").
func helpText(topic string) string {
	switch topic {
	case "validate":
		return validateHelp
	case "format":
		return formatHelp
	case "test":
		return testHelp
	default:
		return topLevelHelp
	}
}

// usageLine returns the one-line usage summary printed alongside a usage
// error, so a mistyped invocation shows the shape it should have taken
// without dumping the entire help text to stderr.
func usageLine() string {
	return "usage: bpe [<policy.hcl>] | validate <policy.hcl> | format <policy.hcl> [--check] | test <policy.hcl> --path <path> --capability <capability> | --help | --version"
}
