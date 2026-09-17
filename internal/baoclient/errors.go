package baoclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"strings"

	openbao "github.com/openbao/openbao/api/v2"

	"github.com/kevinpinscoe/bao-policy-editor/internal/apperr"
)

// The errors this package returns. Each is wrapped in an *apperr.AppError
// carrying the exit code it maps to, so a caller can either ask
// errors.Is for the specific condition or hand the error to
// apperr.CodeOf and get the documented exit status without a second
// translation table.
var (
	// ErrPolicyNotFound means the named policy does not exist on the
	// server. It is deliberately distinct from an authorization failure:
	// OpenBao answers 404 for both "no such policy" and, on some paths,
	// "you may not see this policy", and conflating them would tell a
	// user to go fix a policy that is actually a permissions problem.
	ErrPolicyNotFound = errors.New("baoclient: policy not found")

	// ErrConflict means a check-and-set write was rejected because the
	// policy changed on the server since it was read — or, for a create,
	// because a policy of that name already exists.
	ErrConflict = errors.New("baoclient: the policy changed on the server since it was read")

	// ErrConflictProtectionUnsupported means the server did not provide
	// the version metadata a conflict-safe write depends on.
	//
	// This is returned rather than falling back to a read-compare-write
	// sequence. That fallback is not atomic — another writer can land
	// between the compare and the write — so offering it would mean
	// telling the user their update was conflict-safe when it was not.
	// Kevin's instruction, 2026-09-17.
	ErrConflictProtectionUnsupported = errors.New(
		"baoclient: this server did not return the version metadata a conflict-safe write requires")

	// ErrUnauthorized means the token was rejected (401) or lacks the
	// capability the operation needs (403).
	ErrUnauthorized = errors.New("baoclient: the token was rejected or lacks the required capability")

	// ErrTLS means the TLS handshake or certificate verification failed.
	ErrTLS = errors.New("baoclient: TLS verification failed")
)

// casIndicators are the phrases that identify a check-and-set rejection in
// an error body.
//
// OpenBao's API documentation specifies the `cas` parameter and what it
// does, but does not state the HTTP status code or the error body a failed
// check-and-set produces — verified against
// https://openbao.org/docs/api/system/policies/ on 2026-09-17, which shows
// neither a PATCH example nor any error-response example for this
// endpoint. So this detection is deliberately broad: a dedicated conflict
// status is treated as a conflict on its own, and a 400 is treated as one
// only when the body says so.
//
// The bias is intentional. Reporting a conflict that was really a bad
// request costs the user a re-read and a retry; reporting a bad request
// that was really a conflict tells them their write failed for an
// unrelated reason and invites them to force it.
var casIndicators = []string{
	"check-and-set",
	"check and set",
	"cas parameter",
	"cas mismatch",
	"invalid cas",
	"did not match the current version",
	"already exists",
}

// conflictStatuses are status codes that mean a conflict on this endpoint
// whatever the body says. Neither has another meaning here.
var conflictStatuses = map[int]bool{
	http.StatusConflict:           true, // 409
	http.StatusPreconditionFailed: true, // 412
}

// classify turns an error from the OpenBao client into one of this
// package's typed errors, wrapped in an *apperr.AppError carrying its exit
// code.
//
// token is scrubbed from every message before it is returned — see
// scrub. Nothing in the OpenBao client's own error types is known to
// carry the token (ResponseError holds the method, URL, status and the
// server's error strings, not the request headers), but "known to" is a
// statement about the version that was read, and this package's promise is
// that no error it returns contains the token. Making that true by
// construction is cheaper than re-auditing it on every dependency bump.
func classify(err error, op string, token string) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, context.Canceled):
		return apperr.Interrupted()
	case errors.Is(err, context.DeadlineExceeded):
		return scrub(apperr.Wrap(apperr.ExitOperational,
			"the OpenBao request timed out", err), token)
	}

	if isTLSError(err) {
		return scrub(apperr.Wrap(apperr.ExitOperational,
			"could not establish a trusted TLS connection to the OpenBao server",
			errors.Join(ErrTLS, err)), token)
	}

	var respErr *openbao.ResponseError
	if errors.As(err, &respErr) {
		return scrub(classifyResponse(respErr, op), token)
	}

	return scrub(apperr.Wrap(apperr.ExitOperational, op+" failed", err), token)
}

func classifyResponse(respErr *openbao.ResponseError, op string) error {
	switch {
	case respErr.StatusCode == http.StatusNotFound:
		return apperr.Wrap(apperr.ExitOperational,
			"no such policy on the server", errors.Join(ErrPolicyNotFound, respErr))

	case respErr.StatusCode == http.StatusUnauthorized:
		return apperr.Wrap(apperr.ExitOperational,
			"the OpenBao token was rejected", errors.Join(ErrUnauthorized, respErr))

	case respErr.StatusCode == http.StatusForbidden:
		return apperr.Wrap(apperr.ExitOperational,
			"the OpenBao token lacks the capability this operation needs"+
				" (list on sys/policies/acl, read on sys/policies/acl/*,"+
				" create or update to change a policy, delete to remove one)",
			errors.Join(ErrUnauthorized, respErr))

	case isCASRejection(respErr):
		return apperr.Wrap(apperr.ExitConflict,
			"the policy changed on the server since it was read; refusing to overwrite",
			errors.Join(ErrConflict, respErr))

	default:
		return apperr.Wrap(apperr.ExitOperational, op+" failed", respErr)
	}
}

// isCASRejection reports whether a response error is a check-and-set
// rejection. See casIndicators for why this is matched rather than read
// off a documented status code.
func isCASRejection(respErr *openbao.ResponseError) bool {
	if conflictStatuses[respErr.StatusCode] {
		return true
	}
	if respErr.StatusCode != http.StatusBadRequest {
		return false
	}
	for _, msg := range respErr.Errors {
		lower := strings.ToLower(msg)
		for _, indicator := range casIndicators {
			if strings.Contains(lower, indicator) {
				return true
			}
		}
	}
	return false
}

// isTLSError reports whether err is a certificate or handshake failure, as
// opposed to a connection that was refused or reset.
//
// The distinction matters to the person reading the message: "the
// certificate is not trusted" and "nothing is listening" lead to
// completely different next steps, and collapsing both into "connection
// failed" sends people to the wrong one.
func isTLSError(err error) bool {
	var recordErr tls.RecordHeaderError
	var unknownAuthority x509.UnknownAuthorityError
	var certInvalid x509.CertificateInvalidError
	var hostnameErr *x509.HostnameError
	var verifyErr *tls.CertificateVerificationError

	return errors.As(err, &recordErr) ||
		errors.As(err, &unknownAuthority) ||
		errors.As(err, &certInvalid) ||
		errors.As(err, &hostnameErr) ||
		errors.As(err, &verifyErr)
}

// scrubbedError renders a cleaned message while keeping the original error
// reachable through Unwrap.
//
// Keeping the chain intact is the whole point: errors.Is(err,
// ErrConflict) and apperr.CodeOf(err) must go on working after scrubbing,
// or redaction would quietly convert a typed conflict into an untyped
// failure in exactly the situation where something has already gone
// strangely wrong.
type scrubbedError struct {
	message string
	cause   error
}

func (e *scrubbedError) Error() string { return e.message }
func (e *scrubbedError) Unwrap() error { return e.cause }

// scrub replaces any occurrence of the token in an error's rendered text
// with a redaction marker.
//
// It is a backstop, not the primary defense — nothing in this package puts
// the token into an error in the first place. It exists because an error
// travelling up from a dependency is outside this package's control, and a
// leaked credential is not the kind of thing to leave to inspection.
//
// An empty token scrubs nothing: an empty string is contained in every
// string, and replacing it would corrupt every message.
func scrub(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	rendered := err.Error()
	if !strings.Contains(rendered, token) {
		return err
	}
	return &scrubbedError{
		message: strings.ReplaceAll(rendered, token, "<redacted>"),
		cause:   err,
	}
}
