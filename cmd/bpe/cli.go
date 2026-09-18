package main

import (
	"strings"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
	"github.com/kevinpinscoe/bao-policy-editor/internal/config"
	"github.com/kevinpinscoe/bao-policy-editor/internal/policy"
)

// Kind identifies which command an invocation resolved to. Command
// dispatch (see dispatch.go) switches on this rather than re-parsing
// argv, keeping "what was asked for" and "what to do about it" separate.
type Kind int

const (
	// KindDefault is `bpe` (start empty), `bpe <policy.hcl>` (open a
	// file), or `bpe --remote` (start on the remote policy browser) — the
	// interactive editor.
	KindDefault Kind = iota
	KindValidate
	KindFormat
	KindTest
	KindHelp
	KindVersion
)

// Command is the parsed, structured representation of a single
// invocation. ParseArgs produces it from argv; Execute (dispatch.go) acts
// on it. Nothing between the two needs to touch argv again.
type Command struct {
	Kind Kind

	// PolicyFile is the policy file named on the command line, for
	// KindDefault (when opening a file), KindValidate, KindFormat, and
	// KindTest. Empty for KindDefault's bare "start empty" form.
	PolicyFile string

	// FormatCheck is KindFormat's --check flag: report whether the file
	// would be reformatted without writing it.
	FormatCheck bool

	// TestPath and TestCapability are the required --path/--capability
	// values for KindTest.
	TestPath       string
	TestCapability string

	// HelpTopic is which command's help to show for KindHelp: "" for
	// top-level help, or one of "validate"/"format"/"test".
	HelpTopic string

	// Remote is KindDefault's --remote flag: start the interactive editor
	// on the remote policy browser instead of on a local document. It
	// takes no positional argument — see validateRemote.
	Remote bool

	// ConfigFlags carries the config-related flags the user supplied
	// explicitly (--address, --token, and so on), for config.Resolve.
	ConfigFlags config.Flags
}

func isKnownSubcommand(name string) bool {
	switch name {
	case "validate", "format", "test":
		return true
	default:
		return false
	}
}

// flagKind distinguishes a flag that consumes the next token as its value
// from a boolean switch flag, mirroring the standard library flag
// package's own -flag / -flag=value / -flag=false conventions.
type flagKind int

const (
	flagValue flagKind = iota
	flagBool
)

// globalFlagSpecs are the configuration-related flags recognized anywhere
// in the invocation — before, after, or interspersed with a subcommand —
// because config resolution is the same regardless of which command is
// being run. See scanFlags.
//
// --remote is scanned here too, so that it is recognized wherever it
// appears rather than only in one position. It is not a configuration
// flag, and validateRemote rejects it anywhere it does not belong.
var globalFlagSpecs = map[string]flagKind{
	"remote":          flagBool,
	"address":         flagValue,
	"token":           flagValue,
	"namespace":       flagValue,
	"ca-cert":         flagValue,
	"ca-path":         flagValue,
	"client-cert":     flagValue,
	"client-key":      flagValue,
	"tls-server-name": flagValue,
	"skip-verify":     flagBool,
}

// testFlagSpecs are the flags recognized only within `bpe test`.
var testFlagSpecs = map[string]flagKind{
	"path":       flagValue,
	"capability": flagValue,
}

// formatFlagSpecs are the flags recognized only within `bpe format`.
var formatFlagSpecs = map[string]flagKind{
	"check": flagBool,
}

// scanFlags extracts every token matching a known flag in specs from args,
// wherever it appears, and returns the remaining tokens in their original
// relative order. This is what lets flags and positional arguments be
// reordered — the CLI contract itself requires `bpe test <policy.hcl>
// --path <path> --capability <capability>`, flags after the positional
// filename, which the standard library flag package cannot parse directly
// since it stops at the first non-flag token.
//
// A flag not present in specs is left in remaining rather than rejected
// here, so the caller — which knows what "unknown" means at its own
// level (top-level vs. within a subcommand) — reports it.
func scanFlags(args []string, specs map[string]flagKind) (values map[string]string, remaining []string, err error) {
	values = map[string]string{}
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "-" || !strings.HasPrefix(tok, "-") {
			remaining = append(remaining, tok)
			continue
		}

		name := strings.TrimLeft(tok, "-")
		inlineVal, hasInline := "", false
		if idx := strings.IndexByte(name, '='); idx >= 0 {
			inlineVal = name[idx+1:]
			name = name[:idx]
			hasInline = true
		}

		kind, known := specs[name]
		if !known {
			remaining = append(remaining, tok)
			continue
		}

		switch kind {
		case flagBool:
			if hasInline {
				values[name] = inlineVal
			} else {
				values[name] = "true"
			}
		case flagValue:
			if hasInline {
				values[name] = inlineVal
				continue
			}
			if i+1 >= len(args) {
				return nil, nil, apperr.Usagef("missing value for flag: --%s", name)
			}
			i++
			values[name] = args[i]
		}
	}
	return values, remaining, nil
}

// flagsFromValues converts scanFlags' generic string map into
// config.Flags, taking the address of a fresh local for each present
// value so config.Resolve can distinguish "flag given" from "flag
// absent" (see config.Flags' doc comment).
func flagsFromValues(values map[string]string) config.Flags {
	var f config.Flags
	if v, ok := values["address"]; ok {
		f.Address = &v
	}
	if v, ok := values["token"]; ok {
		f.Token = &v
	}
	if v, ok := values["namespace"]; ok {
		f.Namespace = &v
	}
	if v, ok := values["ca-cert"]; ok {
		f.CACert = &v
	}
	if v, ok := values["ca-path"]; ok {
		f.CAPath = &v
	}
	if v, ok := values["client-cert"]; ok {
		f.ClientCert = &v
	}
	if v, ok := values["client-key"]; ok {
		f.ClientKey = &v
	}
	if v, ok := values["tls-server-name"]; ok {
		f.TLSServerName = &v
	}
	if v, ok := values["skip-verify"]; ok {
		f.SkipVerify = &v
	}
	return f
}

func knownCapabilitiesList() string {
	names := make([]string, len(policy.Capabilities))
	for i, c := range policy.Capabilities {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

func hasHelpFlag(args []string) bool {
	for _, tok := range args {
		if tok == "-h" || tok == "--help" {
			return true
		}
	}
	return false
}

// ParseArgs parses argv (excluding the program name) into a Command. It
// performs no I/O and requires no OpenBao connectivity, config file, or
// filesystem access — only string handling — so it is fully testable by
// calling it directly.
func ParseArgs(args []string) (*Command, error) {
	cmd, err := parseArgs(args)
	if err != nil {
		return nil, err
	}
	if err := validateRemote(cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}

// validateRemote enforces that --remote means exactly one thing.
//
// It starts the interactive editor on the remote browser and takes no
// positional argument. `bpe --remote <name>` is rejected rather than
// treated as a policy name, because `<name>` and `<policy.hcl>` are the
// same token to a parser and telling them apart would mean guessing from a
// file extension — which is precisely the mixed local/remote semantics
// this CLI does not have. A named policy is opened from the browser.
// Kevin's instruction, 2026-09-18.
func validateRemote(cmd *Command) error {
	if !cmd.Remote {
		return nil
	}

	switch cmd.Kind {
	case KindHelp, KindVersion:
		// --help and --version answer from argv alone and are never
		// blocked by another flag being present.
		return nil
	case KindDefault:
		if cmd.PolicyFile != "" {
			return apperr.Usagef(
				"--remote takes no file argument (got %s): it starts the interactive editor on the "+
					"remote policy browser, where a policy is chosen by name", cmd.PolicyFile)
		}
		return nil
	default:
		return apperr.Usage("--remote applies only to the interactive editor, not to validate, format, or test")
	}
}

func parseArgs(args []string) (*Command, error) {
	values, remaining, err := scanFlags(args, globalFlagSpecs)
	if err != nil {
		return nil, err
	}

	cmd, err := parseRemaining(remaining, flagsFromValues(values))
	if err != nil {
		return nil, err
	}
	// Stamped centrally rather than at each construction site, so a
	// command built on a path that forgot about --remote cannot silently
	// lose it and start a local editor instead.
	cmd.Remote = values["remote"] == "true"
	return cmd, nil
}

func parseRemaining(remaining []string, cfgFlags config.Flags) (*Command, error) {
	if len(remaining) == 0 {
		return &Command{Kind: KindDefault, ConfigFlags: cfgFlags}, nil
	}

	switch remaining[0] {
	case "--help", "-h":
		if len(remaining) > 1 {
			return nil, apperr.Usagef("unexpected argument: %s", remaining[1])
		}
		return &Command{Kind: KindHelp, ConfigFlags: cfgFlags}, nil

	case "--version":
		if len(remaining) > 1 {
			return nil, apperr.Usagef("unexpected argument: %s", remaining[1])
		}
		return &Command{Kind: KindVersion, ConfigFlags: cfgFlags}, nil

	case "help":
		rest := remaining[1:]
		switch {
		case len(rest) == 0:
			return &Command{Kind: KindHelp, ConfigFlags: cfgFlags}, nil
		case len(rest) == 1 && isKnownSubcommand(rest[0]):
			return &Command{Kind: KindHelp, HelpTopic: rest[0], ConfigFlags: cfgFlags}, nil
		case len(rest) == 1:
			return nil, apperr.Usagef("unknown command: %s", rest[0])
		default:
			return nil, apperr.Usagef("unexpected argument: %s", rest[1])
		}

	case "validate":
		return parseFileOnlySubcommand(KindValidate, "validate", remaining[1:], cfgFlags)

	case "format":
		return parseFormatSubcommand(remaining[1:], cfgFlags)

	case "test":
		return parseTestSubcommand(remaining[1:], cfgFlags)
	}

	if strings.HasPrefix(remaining[0], "-") {
		return nil, apperr.Usagef("unknown flag: %s", remaining[0])
	}

	if len(remaining) > 1 {
		return nil, apperr.Usagef("unknown command: %s", remaining[0])
	}

	return &Command{Kind: KindDefault, PolicyFile: remaining[0], ConfigFlags: cfgFlags}, nil
}

// parseFileOnlySubcommand parses `bpe validate <policy.hcl>` and
// `bpe format <policy.hcl>`, which take exactly one positional argument
// and no flags of their own.
func parseFileOnlySubcommand(kind Kind, name string, args []string, cfgFlags config.Flags) (*Command, error) {
	if hasHelpFlag(args) {
		return &Command{Kind: KindHelp, HelpTopic: name, ConfigFlags: cfgFlags}, nil
	}

	var positional []string
	for _, tok := range args {
		if strings.HasPrefix(tok, "-") {
			return nil, apperr.Usagef("unknown flag: %s", tok)
		}
		positional = append(positional, tok)
	}

	switch len(positional) {
	case 0:
		return nil, apperr.Usagef("missing required argument: <policy.hcl> (usage: bpe %s <policy.hcl>)", name)
	case 1:
		return &Command{Kind: kind, PolicyFile: positional[0], ConfigFlags: cfgFlags}, nil
	default:
		return nil, apperr.Usagef("unexpected argument: %s", positional[1])
	}
}

// parseFormatSubcommand parses `bpe format <policy.hcl> [--check]`,
// accepting --check before or after the positional filename, unlike
// parseFileOnlySubcommand's other two callers (validate has no flags of
// its own).
func parseFormatSubcommand(args []string, cfgFlags config.Flags) (*Command, error) {
	if hasHelpFlag(args) {
		return &Command{Kind: KindHelp, HelpTopic: "format", ConfigFlags: cfgFlags}, nil
	}

	values, remaining, err := scanFlags(args, formatFlagSpecs)
	if err != nil {
		return nil, err
	}

	var positional []string
	for _, tok := range remaining {
		if strings.HasPrefix(tok, "-") {
			return nil, apperr.Usagef("unknown flag: %s", tok)
		}
		positional = append(positional, tok)
	}

	switch len(positional) {
	case 0:
		return nil, apperr.Usage("missing required argument: <policy.hcl> (usage: bpe format <policy.hcl> [--check])")
	case 1:
		return &Command{
			Kind:        KindFormat,
			PolicyFile:  positional[0],
			FormatCheck: values["check"] == "true",
			ConfigFlags: cfgFlags,
		}, nil
	default:
		return nil, apperr.Usagef("unexpected argument: %s", positional[1])
	}
}

// parseTestSubcommand parses
// `bpe test <policy.hcl> --path <path> --capability <capability>`,
// accepting --path/--capability before or after the positional filename.
func parseTestSubcommand(args []string, cfgFlags config.Flags) (*Command, error) {
	if hasHelpFlag(args) {
		return &Command{Kind: KindHelp, HelpTopic: "test", ConfigFlags: cfgFlags}, nil
	}

	values, remaining, err := scanFlags(args, testFlagSpecs)
	if err != nil {
		return nil, err
	}

	var positional []string
	for _, tok := range remaining {
		if strings.HasPrefix(tok, "-") {
			return nil, apperr.Usagef("unknown flag: %s", tok)
		}
		positional = append(positional, tok)
	}

	var policyFile string
	switch len(positional) {
	case 0:
		return nil, apperr.Usage("missing required argument: <policy.hcl> (usage: bpe test <policy.hcl> --path <path> --capability <capability>)")
	case 1:
		policyFile = positional[0]
	default:
		return nil, apperr.Usagef("unexpected argument: %s", positional[1])
	}

	path := values["path"]
	if path == "" {
		return nil, apperr.Usage("missing required flag: --path")
	}
	capability := values["capability"]
	if capability == "" {
		return nil, apperr.Usage("missing required flag: --capability")
	}
	if !policy.Capability(capability).Known() {
		return nil, apperr.Usagef("unknown capability: %s (known capabilities: %s)", capability, knownCapabilitiesList())
	}

	return &Command{
		Kind:           KindTest,
		PolicyFile:     policyFile,
		TestPath:       path,
		TestCapability: capability,
		ConfigFlags:    cfgFlags,
	}, nil
}
