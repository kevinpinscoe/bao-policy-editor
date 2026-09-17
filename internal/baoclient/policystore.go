package baoclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	openbao "github.com/openbao/openbao/api/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
)

// aclPolicyPath is the API path for the ACL policy endpoints.
const aclPolicyPath = "sys/policies/acl"

// mergePatchContentType is what a PATCH to a logical endpoint is sent as.
//
// OpenBao's API documentation for sys/policies/acl documents PATCH's
// parameters but shows no PATCH example and states no Content-Type
// requirement (checked 2026-09-17). This is the content type the official
// Go client's own PATCH path (Logical.JSONMergePatch) sets, so it is the
// least-invented choice available; it is named here rather than inlined so
// there is one place to change if a live server disagrees.
const mergePatchContentType = "application/merge-patch+json"

// Revision is the server-side state of a policy at the moment it was read.
//
// It is what makes an update conflict-safe: the version that came back
// from the read is sent as the `cas` parameter on the write, and OpenBao
// rejects the write if the policy has moved on since. See
// https://openbao.org/docs/api/system/policies/.
type Revision struct {
	// Version is the policy's version as the server reported it. Policies
	// that predate versioning start at 0, so 0 is a real version and not a
	// "missing" sentinel — HasVersion is what answers that.
	Version int

	// HasVersion reports whether the server actually supplied a version.
	// Without it there is no conflict-safe write to be had, and Update
	// refuses rather than pretending otherwise.
	HasVersion bool

	// Modified is when the server last changed the policy, if it said.
	Modified time.Time

	// CASRequired is the policy's own cas_required setting, as the server
	// reported it. BPE reads this and passes it back untouched; it never
	// turns it on for a policy that did not already have it, because that
	// would change the server-side rules for every other client of that
	// policy on BPE's say-so. Kevin's instruction, 2026-09-17.
	CASRequired bool

	// ContentHash is a SHA-256 of the policy body as read, hex-encoded.
	//
	// It is for diagnostics — telling "the same edit arrived twice" from
	// "two different edits collided" when reading a log. It is deliberately
	// **not** used as a conflict check: comparing it would be a
	// read-then-write sequence with a race in the middle, and the server's
	// own CAS has no such gap.
	ContentHash string
}

// Policy is one ACL policy as read from the server.
type Policy struct {
	Name     string
	Body     string
	Revision Revision

	// Warnings are whatever the server attached to the response. They are
	// carried rather than dropped because OpenBao uses them to report
	// things that did not fail but that the caller should know.
	Warnings []string
}

// PolicyList is the result of listing policy names.
type PolicyList struct {
	Names    []string
	Warnings []string
}

// WriteResult is what a create or update returned.
type WriteResult struct {
	// Revision carries version metadata if the write response included
	// any. A server that returns nothing leaves HasVersion false; the
	// caller re-reads if it needs the new version.
	Revision Revision
	Warnings []string
}

// PolicyStore is the remote-policy surface the rest of BPE talks to.
//
// It is an interface so the terminal editor (FSM-17) can be built and
// tested against a fake without a server, and so this package's own tests
// are the only place the real HTTP path is exercised. Every method takes a
// context: these are the only operations in BPE that touch a network, and
// every one of them must be cancellable.
type PolicyStore interface {
	List(ctx context.Context) (PolicyList, error)
	Read(ctx context.Context, name string) (Policy, error)

	// Create writes a policy that must not already exist. It sends
	// cas = -1, so a policy that appeared between the caller's list and
	// this call is a conflict rather than something to overwrite.
	Create(ctx context.Context, name, body string) (WriteResult, error)

	// Update writes a policy that already exists, sending the version from
	// rev as the check-and-set value. It returns
	// ErrConflictProtectionUnsupported rather than writing if rev carries
	// no version.
	Update(ctx context.Context, name, body string, rev Revision) (WriteResult, error)

	// Delete removes a policy.
	//
	// This endpoint has no check-and-set equivalent, so deletion cannot be
	// made conflict-safe the way an update can: a policy changed by
	// someone else a moment earlier is deleted just the same. Any check
	// before it would be advisory rather than atomic, so none is offered
	// here — the confirmation belongs in the interface that asks for it
	// (FSM-17), where it can be a decision rather than a guess.
	Delete(ctx context.Context, name string) error
}

// Ensure the concrete client satisfies the interface it publishes.
var _ PolicyStore = (*Client)(nil)

// List returns the names of every ACL policy the token can see.
func (c *Client) List(ctx context.Context) (PolicyList, error) {
	secret, err := c.api.Logical().ListWithContext(ctx, aclPolicyPath)
	if err != nil {
		return PolicyList{}, c.fail(err, "listing policies")
	}
	if secret == nil || secret.Data == nil {
		// An empty instance answers 404, which the client surfaces as a
		// nil secret rather than an error. No policies is not a failure.
		return PolicyList{}, nil
	}

	raw, ok := secret.Data["keys"]
	if !ok {
		return PolicyList{Warnings: secret.Warnings}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return PolicyList{}, c.malformed("listing policies", `"keys" was not a list`)
	}

	names := make([]string, 0, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok {
			return PolicyList{}, c.malformed("listing policies", `"keys" contained a non-string entry`)
		}
		names = append(names, name)
	}
	return PolicyList{Names: names, Warnings: secret.Warnings}, nil
}

// Read fetches one policy together with the revision metadata a
// conflict-safe update depends on.
//
// It goes through Logical rather than the client's Sys().GetPolicy
// helper. That helper returns only the policy string and discards the rest
// of the response — including version, modified and cas_required — which
// would leave every update in this package unable to send a `cas` at all.
func (c *Client) Read(ctx context.Context, name string) (Policy, error) {
	secret, err := c.api.Logical().ReadWithContext(ctx, policyPath(name))
	if err != nil {
		return Policy{}, c.fail(err, "reading policy "+name)
	}
	if secret == nil || secret.Data == nil {
		// A missing policy comes back as a nil secret, not an error.
		return Policy{}, c.notFound(name)
	}

	body, err := stringField(secret.Data, "policy")
	if err != nil {
		return Policy{}, c.malformed("reading policy "+name, err.Error())
	}

	return Policy{
		Name:     name,
		Body:     body,
		Revision: revisionFrom(secret.Data, body),
		Warnings: secret.Warnings,
	}, nil
}

// Create writes a new policy, refusing to overwrite an existing one.
//
// cas = -1 is OpenBao's documented "this must be a create" value: the
// write fails if anything is already there. That is what makes this safe
// against a policy of the same name appearing between a caller listing the
// policies and deciding the name was free.
func (c *Client) Create(ctx context.Context, name, body string) (WriteResult, error) {
	return c.write(ctx, http.MethodPost, name, map[string]any{
		"policy": body,
		"cas":    -1,
	}, "creating policy "+name)
}

// Update writes an existing policy, sending the version from rev as the
// check-and-set value.
//
// PATCH rather than POST: OpenBao documents POST as resetting unspecified
// fields to their defaults and PATCH as preserving them. A policy may
// carry an expiration, a ttl, cas_required, or the identity-template
// flags, none of which BPE models — sending POST with only `policy` would
// silently clear them. Kevin's instruction, 2026-09-17.
//
// cas_required is deliberately not sent. It is the server's own per-policy
// setting; BPE supplies `cas` for its own writes but does not turn the
// requirement on for other clients of the same policy.
func (c *Client) Update(ctx context.Context, name, body string, rev Revision) (WriteResult, error) {
	if !rev.HasVersion {
		return WriteResult{}, c.unsupportedConflictProtection(name)
	}
	return c.write(ctx, http.MethodPatch, name, map[string]any{
		"policy": body,
		"cas":    rev.Version,
	}, "updating policy "+name)
}

// Delete removes a policy. See PolicyStore.Delete on why there is no
// check-and-set here.
func (c *Client) Delete(ctx context.Context, name string) error {
	if _, err := c.api.Logical().DeleteWithContext(ctx, policyPath(name)); err != nil {
		return c.fail(err, "deleting policy "+name)
	}
	return nil
}

// write issues a POST or PATCH to a policy path and parses whatever came
// back.
//
// It builds the request through the client rather than Logical's own
// helpers so the HTTP method is explicit: Logical.Write sends PUT, and
// while OpenBao accepts PUT and POST alike for a logical write, the
// create/update distinction here rests on the documented POST and PATCH
// semantics, and a reader should be able to see which one is on the wire
// without going through a second package to find out.
func (c *Client) write(ctx context.Context, method, name string, body map[string]any, op string) (WriteResult, error) {
	req := c.api.NewRequest(method, "/v1/"+policyPath(name))
	if method == http.MethodPatch {
		req.Headers.Set("Content-Type", mergePatchContentType)
	}
	if err := req.SetJSONBody(body); err != nil {
		return WriteResult{}, c.fail(err, op)
	}

	//nolint:staticcheck // RawRequestWithContext is the only exported path
	// for a request whose HTTP method this package chooses; Logical's
	// helpers hard-code PUT and PATCH respectively.
	resp, err := c.api.RawRequestWithContext(ctx, req)
	if resp != nil {
		defer resp.Body.Close() //nolint:errcheck
	}
	if err != nil {
		return WriteResult{}, c.fail(err, op)
	}

	secret, err := openbao.ParseSecret(resp.Body)
	if err != nil {
		// A successful write with an unparseable body is still a
		// successful write — the policy is changed either way, and
		// reporting a failure would invite the caller to retry a write
		// that already happened.
		return WriteResult{}, nil
	}
	if secret == nil {
		return WriteResult{}, nil
	}

	return WriteResult{
		Revision: revisionFrom(secret.Data, ""),
		Warnings: secret.Warnings,
	}, nil
}

// revisionFrom extracts the version metadata OpenBao returned.
//
// Every field is optional and independently absent: a server that returns
// no version leaves HasVersion false, which is what Update checks before
// it is willing to claim a write is conflict-safe. Nothing here invents a
// default — a missing version is reported as missing rather than assumed
// to be zero, because zero is a real version on this endpoint.
func revisionFrom(data map[string]any, body string) Revision {
	rev := Revision{}
	if body != "" {
		sum := sha256.Sum256([]byte(body))
		rev.ContentHash = hex.EncodeToString(sum[:])
	}
	if data == nil {
		return rev
	}

	if version, ok := intField(data, "version"); ok {
		rev.Version = version
		rev.HasVersion = true
	}
	if required, ok := data["cas_required"].(bool); ok {
		rev.CASRequired = required
	}
	if modified, ok := data["modified"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, modified); err == nil {
			rev.Modified = parsed
		}
	}
	return rev
}

// intField reads a numeric field, tolerating the several shapes JSON
// decoding can produce for one.
//
// encoding/json yields float64 by default, json.Number when the decoder is
// configured for it (which the OpenBao client is, for responses), and a
// plain int never — so checking only one of them silently drops the
// version on half the paths through the client.
func intField(data map[string]any, key string) (int, bool) {
	switch value := data[key].(type) {
	case json.Number:
		n, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	case float64:
		return int(value), true
	case int:
		return value, true
	case int64:
		return int(value), true
	default:
		return 0, false
	}
}

func stringField(data map[string]any, key string) (string, error) {
	raw, ok := data[key]
	if !ok {
		return "", fmt.Errorf("the response contained no %q field", key)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("the %q field was not a string", key)
	}
	return value, nil
}

func policyPath(name string) string { return aclPolicyPath + "/" + name }

// fail routes an error from the OpenBao client through this package's
// classification, with the token available for scrubbing.
func (c *Client) fail(err error, op string) error {
	return classify(err, op, c.token)
}

func (c *Client) notFound(name string) error {
	return apperr.Wrap(apperr.ExitOperational,
		"no such policy on the server", fmt.Errorf("%w: %s", ErrPolicyNotFound, name))
}

func (c *Client) malformed(op, detail string) error {
	return apperr.Newf(apperr.ExitOperational,
		"the OpenBao server's response could not be understood while %s: %s", op, detail)
}

func (c *Client) unsupportedConflictProtection(name string) error {
	return apperr.Wrap(apperr.ExitOperational,
		"refusing to update "+name+" without conflict protection",
		errors.Join(ErrConflictProtectionUnsupported,
			errors.New("re-read the policy first, or use a server that reports policy versions")))
}
