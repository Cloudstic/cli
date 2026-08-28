package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
)

// DeleteError records one key a batch delete could not confirm gone.
//
// "Could not confirm" rather than "was not deleted" is the deliberate reading:
// a request that failed in transit may well have deleted the object, and a
// response that mentions a key neither as deleted nor as failed says nothing
// either way. A garbage collector must round both of those towards "still
// there", because the opposite rounding is what makes it report space it did
// not reclaim.
type DeleteError struct {
	Key string
	Err error
}

func (e DeleteError) Error() string { return fmt.Sprintf("delete %s: %v", e.Key, e.Err) }

func (e DeleteError) Unwrap() error { return e.Err }

// DeleteErrors is what a BatchDeleter returns when it can name the keys that
// failed. It is the whole point of the interface: a batch delete that
// collapsed a thousand keys into one error would leave a caller unable to say
// which objects survived, and unable to tell "one key was denied" from "the
// request never landed".
//
// It unwraps to its elements, so errors.Is and errors.As reach the underlying
// backend errors.
type DeleteErrors []DeleteError

func (e DeleteErrors) Error() string {
	switch len(e) {
	case 0:
		return "no delete failures"
	case 1:
		return e[0].Error()
	}
	// Naming the first and counting the rest: the whole list can be a thousand
	// entries long, and a caller that wants them all has Keys().
	return fmt.Sprintf("%v (and %d more keys)", e[0], len(e)-1)
}

func (e DeleteErrors) Unwrap() []error {
	errs := make([]error, len(e))
	for i, de := range e {
		errs[i] = de
	}
	return errs
}

// Keys returns the keys that could not be confirmed deleted.
func (e DeleteErrors) Keys() []string {
	keys := make([]string, len(e))
	for i, de := range e {
		keys[i] = de.Key
	}
	return keys
}

// FailedDeletes reports the keys an error from DeleteAll names as not confirmed
// deleted, with the cause recorded for each.
//
// ok is false when the error carries no per-key detail — a transport failure
// covering the whole call, a permission error, an implementation that
// collapsed its failures. Nothing about which keys survived may then be
// assumed, so a caller doing accounting must credit none of the batch rather
// than all of it. That asymmetry is the reason this returns a second value
// instead of an empty slice: "no keys failed" and "which keys failed is
// unknown" are opposite answers for a garbage collector.
func FailedDeletes(err error) (failed DeleteErrors, ok bool) {
	if err == nil {
		return nil, true
	}
	var de DeleteErrors
	if errors.As(err, &de) {
		return de, true
	}
	return nil, false
}

// DeleteAll deletes keys through s, using its BatchDeleter capability when it
// has one and looping otherwise, so a caller needs no fallback branch.
//
// The capability is looked up on s itself and never by unwrapping to an inner
// store. Delete is not a passthrough at every layer — PackStore's rewrites a
// catalog rather than touching the backend — so reaching past a wrapper to the
// batch-capable backend beneath it would delete objects the wrapper still
// believes it owns. Each wrapper that *can* forward safely says so by
// implementing BatchDeleter itself.
func DeleteAll(ctx context.Context, s ObjectStore, keys []string) error {
	if bd, ok := s.(BatchDeleter); ok {
		return bd.DeleteAll(ctx, keys)
	}
	return DeleteEach(ctx, keys, s.Delete)
}

// DeleteEach is the BatchDeleter implementation for a backend with no bulk
// primitive: one Delete per key, collecting every failure instead of stopping
// at the first, so the result still says exactly which keys survived.
//
// A key that is already gone counts as deleted, matching what S3's
// DeleteObjects reports for a missing key. Backends differ here — os.Remove
// and sftp Remove both fail on a missing file — and letting that difference
// through would mean a prune that succeeds on S3 and fails on a local store
// after an interrupted earlier run.
func DeleteEach(ctx context.Context, keys []string, del func(context.Context, string) error) error {
	var failures DeleteErrors
	for i, key := range keys {
		if err := ctx.Err(); err != nil {
			// Issuing the remaining calls on a dead context would only produce
			// the same error more slowly, but they are still unconfirmed and
			// have to be reported as such.
			for _, rest := range keys[i:] {
				failures = append(failures, DeleteError{Key: rest, Err: err})
			}
			break
		}
		if err := del(ctx, key); err != nil && !isAlreadyGone(err) {
			failures = append(failures, DeleteError{Key: key, Err: err})
		}
	}
	if len(failures) > 0 {
		return failures
	}
	return nil
}

// isAlreadyGone reports whether err means the object was not there to delete.
func isAlreadyGone(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrNotFound)
}

// UnconfirmedDeletes builds the DeleteErrors for a call that failed as a whole,
// attributing the same cause to every key it covered. Backends use it when a
// bulk request fails in transit: the keys in that request are unconfirmed, and
// saying so per key keeps the keys in the requests that did land creditable.
func UnconfirmedDeletes(keys []string, err error) DeleteErrors {
	failures := make(DeleteErrors, len(keys))
	for i, key := range keys {
		failures[i] = DeleteError{Key: key, Err: err}
	}
	return failures
}
