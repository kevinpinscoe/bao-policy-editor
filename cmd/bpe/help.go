package main

const topLevelHelp = `bpe — a terminal interface for creating, editing, validating, and testing
OpenBao HCL ACL policies.

Usage:
  bpe                                     Start the interactive editor with an empty policy
  bpe <policy.hcl>                        Start the interactive editor, opening a policy file
  bpe validate <policy.hcl>               Validate a policy without starting the interactive editor
  bpe format <policy.hcl>                 Format a policy file
  bpe test <policy.hcl> --path <path> --capability <capability>
                                           Simulate an effective-access check
  bpe help [command]                      Show this help, or help for one command
  bpe --help                              Show this help
  bpe --version                           Show version information

The interactive editor, validate, format, and test are scaffolded for this
release and report "not implemented yet" rather than performing any action —
see bpe <command> --help for each command's arguments, and RUNBOOK.md for
current limitations.

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

Not implemented yet — see README.md's current limitations.
`

const formatHelp = `bpe format <policy.hcl> — format a policy file.

Usage:
  bpe format <policy.hcl>

Not implemented yet — see README.md's current limitations.
`

const testHelp = `bpe test <policy.hcl> --path <path> --capability <capability> — simulate
an effective-access check against a local policy file.

Usage:
  bpe test <policy.hcl> --path <path> --capability <capability>

--path and --capability may appear before or after the policy file.

Not implemented yet — see README.md's current limitations.
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
	return "usage: bpe [<policy.hcl>] | validate <policy.hcl> | format <policy.hcl> | test <policy.hcl> --path <path> --capability <capability> | --help | --version"
}
