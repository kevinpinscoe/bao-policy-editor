package baoclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	openbao "github.com/openbao/openbao/api/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
)

// aclPolicyPath is the API path for the ACL policy endpoints.
const aclPolicyPath = "sys/policies/acl"

// Updates are POST, not PATCH.
//
// BPE sent PATCH until 2026-09-18, on the strength of OpenBao's API
// documentation listing PATCH's parameters for this endpoint. Measured
// against a live OpenBao 2.5.2, sys/policies/acl/:name does not implement
// it: the request comes back 405 "unsupported operation", so every remote
// update failed. The content type was never the problem — the server names
// application/merge-patch+json itself and answers 415 for anything else,
// and PATCH works on kv v2 on the same server, so this is one endpoint
// declining a verb rather than a server lacking it.
//
// Newer OpenBao releases document PATCH here. BPE uses the POST form
// regardless, because it is accepted by both and nothing is gained by
// discovering support at runtime: a fallback would mean sending a request
// expected to fail, on a path where the failure of a *write* is exactly
// what must not be guessed at. Kevin's instruction, 2026-09-18.
//
// The cost is that POST resets what it is not sent — see Metadata.

// Metadata is the writable policy fields BPE does not model, as the server
// reported them on a read.
//
// It exists because an update is a POST, and POST resets every field it is
// not sent. Measured against OpenBao 2.5.2 on 2026-09-18: a POST carrying
// only `policy` and `cas` cleared a policy's `expiration` — whether set
// directly or derived from a `ttl` — and cleared `cas_required`. Echoing
// each field back on the same POST preserved it while still updating the
// body, which is what BPE does.
//
// # Presence is not the same as a false value
//
// Every field is a pointer so that "the server did not report this" and
// "the server reported false" stay distinguishable. Collapsing them would
// make the two indistinguishable in exactly the direction that loses data:
// a policy with cas_required unset and one with it explicitly false would
// both round-trip as false, and, worse, a field this version of OpenBao
// does not report at all would be sent as a zero value and thereby set.
//
// Only fields the server actually reported are sent. Nothing here is
// invented, defaulted, or carried across from another policy.
type Metadata struct {
	// Expiration is the policy's absolute expiry, exactly as the server
	// rendered it. It is kept as the server's own string rather than
	// parsed and reformatted, because reformatting a timestamp BPE does
	// not otherwise interpret can only lose precision or shift a zone.
	Expiration *string

	// CASRequired is the policy's own cas_required setting.
	//
	// Until 2026-09-18 BPE read this and deliberately never sent it, so as
	// not to turn the requirement on for other clients of the same policy.
	// That reasoning still holds for *enabling* it — and echoing back the
	// value the server just reported does not enable anything. Omitting it
	// from a POST is what changes the setting, by clearing it. Kevin's
	// instruction, 2026-09-18.
	CASRequired *bool

	// AllowWildcardsInIdentityTemplates and
	// AllowSlashesInIdentityTemplates are the identity-template flags,
	// carried on the same terms: preserved when the server reports them,
	// absent otherwise.
	AllowWildcardsInIdentityTemplates *bool
	AllowSlashesInIdentityTemplates   *bool

	// Unpreservable names the fields the server did report but in a shape
	// this client does not recognize — a numeric expiration, a
	// cas_required arriving as the string "true".
	//
	// Absent and unparseable are deliberately not the same thing. Both
	// leave the field's pointer nil, and until 2026-09-18 both were
	// treated as "the server did not mention it", so an update omitted the
	// field and POST cleared it. A field that was never reported is
	// genuinely nothing to preserve; a field that was reported and not
	// understood is a value about to be destroyed. Recording the second
	// case here is what lets Update refuse instead of guessing.
	//
	// The names are the server's own, in writableFields order, so the
	// error can say which field it could not carry.
	Unpreservable []string
}

// Preservable reports whether an update built from this metadata can put
// back everything the server reported.
func (m Metadata) Preservable() bool { return len(m.Unpreservable) == 0 }

// writableFields is the response fields Metadata round-trips, and the only
// ones an update sends besides `policy` and `cas`.
//
// The rest of a read's response is envelope — `name`, `version`,
// `modified`, `warnings` — describing the policy rather than configuring
// it, and sending any of it back would at best be ignored and at worst
// rejected.
//
// `ttl` is deliberately absent. The server stores it as an absolute
// `expiration`, so replaying the original relative value on every update
// would push the expiry further out each time a policy was edited — a
// policy meant to lapse would quietly become permanent. The absolute
// expiration is preserved instead. Kevin's instruction, 2026-09-18.
//
// The list is a table rather than plain names so that metadataFrom is
// driven by it. A second, hand-written list of the same four fields is a
// list that drifts: adding a field to one and forgetting the other is how
// a field ends up read but never sent, which on a POST means cleared.
var writableFields = []writableField{
	{
		name: "expiration",
		read: func(m *Metadata, raw any) bool {
			v, ok := raw.(string)
			if ok {
				m.Expiration = &v
			}
			return ok
		},
	},
	{
		name: "cas_required",
		read: func(m *Metadata, raw any) bool {
			v, ok := raw.(bool)
			if ok {
				m.CASRequired = &v
			}
			return ok
		},
	},
	{
		name: "allow_wildcards_in_identity_templates",
		read: func(m *Metadata, raw any) bool {
			v, ok := raw.(bool)
			if ok {
				m.AllowWildcardsInIdentityTemplates = &v
			}
			return ok
		},
	},
	{
		name: "allow_slashes_in_identity_templates",
		read: func(m *Metadata, raw any) bool {
			v, ok := raw.(bool)
			if ok {
				m.AllowSlashesInIdentityTemplates = &v
			}
			return ok
		},
	},
}

// writableField is one preserved field: the name the server uses for it
// and how to take its value out of a response.
//
// read reports whether the raw value was a shape this client understands.
// It never coerces one shape into another — a numeric expiration is not
// formatted into a timestamp and a "true" string is not parsed into a
// boolean, because either would mean writing back a value the server
// never sent.
type writableField struct {
	name string
	read func(*Metadata, any) bool
}

// metadataFrom reads the writable fields a response actually carried, and
// records the ones it carried in an unrecognized shape.
//
// A JSON null counts as absent rather than unparseable. `"expiration":
// null` says the policy has no expiration, so omitting the field from the
// update leaves it exactly as it already is — there is nothing there to
// destroy, and refusing the update would block a save that cannot lose
// anything. Kevin's decision, 2026-09-18.
func metadataFrom(data map[string]any) Metadata {
	var m Metadata
	if data == nil {
		return m
	}
	for _, field := range writableFields {
		raw, present := data[field.name]
		if !present || raw == nil {
			continue
		}
		if !field.read(&m, raw) {
			m.Unpreservable = append(m.Unpreservable, field.name)
		}
	}
	return m
}

// applyTo adds each reported field to an update's request body, leaving
// out the ones the server never mentioned.
func (m Metadata) applyTo(body map[string]any) {
	if m.Expiration != nil {
		body["expiration"] = *m.Expiration
	}
	if m.CASRequired != nil {
		body["cas_required"] = *m.CASRequired
	}
	if m.AllowWildcardsInIdentityTemplates != nil {
		body["allow_wildcards_in_identity_templates"] = *m.AllowWildcardsInIdentityTemplates
	}
	if m.AllowSlashesInIdentityTemplates != nil {
		body["allow_slashes_in_identity_templates"] = *m.AllowSlashesInIdentityTemplates
	}
}

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

	// Metadata is the writable policy state the server reported, carried so
	// an update can put it back. See Metadata.
	Metadata Metadata

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
	result, err := c.write(ctx, http.MethodPost, name, map[string]any{
		"policy": body,
		"cas":    -1,
	}, "creating policy "+name)
	if err != nil {
		return WriteResult{}, err
	}
	return c.revisionAfterWrite(ctx, name, body, result), nil
}

// Update writes an existing policy with POST, sending the version from rev
// as the check-and-set value and putting back the writable metadata that
// read reported.
//
// POST is the verb because it is the one this endpoint implements; see the
// comment at the top of this file. Its cost is that it resets what it is
// not sent, which is why rev.Metadata comes along: those fields were read
// from the server moments ago and are handed straight back, unexamined and
// unmodified.
//
// Nothing is invented. A field the server did not report is not sent, so
// an OpenBao that knows nothing of the identity-template flags is never
// told about them, and a policy with no expiration does not acquire one.
func (c *Client) Update(ctx context.Context, name, body string, rev Revision) (WriteResult, error) {
	// Both refusals happen before a request is built, so a policy BPE
	// cannot write safely is never touched at all.
	if !rev.Metadata.Preservable() {
		return WriteResult{}, c.unpreservableMetadata(name, rev.Metadata.Unpreservable)
	}
	if !rev.HasVersion {
		return WriteResult{}, c.unsupportedConflictProtection(name)
	}

	payload := map[string]any{
		"policy": body,
		"cas":    rev.Version,
	}
	rev.Metadata.applyTo(payload)

	result, err := c.write(ctx, http.MethodPost, name, payload, "updating policy "+name)
	if err != nil {
		return WriteResult{}, err
	}
	return c.revisionAfterWrite(ctx, name, body, result), nil
}

// revisionAfterWrite fills in the version and metadata a write did not
// report, by reading the policy back.
//
// It is needed because this endpoint answers a successful write with
// 204 and no body — measured against OpenBao 2.5.2 — so the write itself
// says nothing about the version it produced. Without this, every second
// update in a session would be refused for lack of conflict protection,
// and the metadata carried into the next update would be empty, which is
// how a POST silently clears it.
//
// The re-read has to prove it read back what this client just wrote.
// There is a gap between the write and the read, and another client can
// write in it — so the body that comes back is not necessarily BPE's. An
// earlier version of this function adopted whatever version the read
// reported and left a comment claiming the next check-and-set would catch
// the difference. It would not: the version adopted is the *current* one,
// so the next update's `cas` matches and the other client's change is
// overwritten with no conflict ever shown.
//
// So the body is compared, exactly, and the revision is adopted only if it
// is the one BPE wrote. The comparison is deliberately byte-for-byte: a
// server that normalizes what it stores would trip it, and being sent back
// to re-open the policy is the right outcome there too — BPE would
// otherwise be holding a revision for content it has never seen.
//
// A failed re-read is not a failed write, and neither is a mismatched one.
// The policy has already changed on the server, so the write's success is
// returned as it stands; the caller sees a revision with no version and is
// told to re-open the policy before updating it again.
func (c *Client) revisionAfterWrite(ctx context.Context, name, written string, result WriteResult) WriteResult {
	if result.Revision.HasVersion {
		return result
	}
	policy, err := c.Read(ctx, name)
	if err != nil {
		return result
	}
	if policy.Body != written {
		result.Revision = Revision{}
		result.Warnings = append(result.Warnings,
			name+" changed on the server again immediately after this write, so its"+
				" version could not be established; re-open it before editing it further")
		return result
	}
	result.Revision = policy.Revision
	return result
}

// Delete removes a policy. See PolicyStore.Delete on why there is no
// check-and-set here.
func (c *Client) Delete(ctx context.Context, name string) error {
	if _, err := c.api.Logical().DeleteWithContext(ctx, policyPath(name)); err != nil {
		return c.fail(err, "deleting policy "+name)
	}
	return nil
}

// write issues a POST to a policy path and parses whatever came back.
//
// It builds the request through the client rather than Logical's own
// helpers so the HTTP method is explicit: Logical.Write sends PUT, and
// while OpenBao accepts PUT and POST alike for a logical write, a reader
// should be able to see what is on the wire without going through a second
// package to find out.
func (c *Client) write(ctx context.Context, method, name string, body map[string]any, op string) (WriteResult, error) {
	req := c.api.NewRequest(method, "/v1/"+policyPath(name))
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
	rev.Metadata = metadataFrom(data)
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

// unpreservableMetadata refuses an update that would have to guess at, or
// silently drop, a field the server reported in an unrecognized shape.
//
// The message names the fields, because the person reading it can only act
// on it by looking at the policy on the server — and "some metadata" would
// not tell them where to look.
func (c *Client) unpreservableMetadata(name string, fields []string) error {
	return apperr.Wrap(apperr.ExitOperational,
		"refusing to update "+name+": this server reported "+strings.Join(fields, ", ")+
			" in a form BPE cannot send back, and an update would clear what it cannot preserve",
		errors.Join(ErrMetadataNotPreservable,
			errors.New("inspect the policy on the server and correct the field, or update it there")))
}

func (c *Client) unsupportedConflictProtection(name string) error {
	return apperr.Wrap(apperr.ExitOperational,
		"refusing to update "+name+" without conflict protection",
		errors.Join(ErrConflictProtectionUnsupported,
			errors.New("re-read the policy first, or use a server that reports policy versions")))
}
