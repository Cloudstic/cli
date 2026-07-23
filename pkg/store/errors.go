package store

import "errors"

// ErrNotFound is the sentinel error returned by Get when the requested key
// does not exist. Each backend translates its own not-found signal — a
// missing file, an S3 NoSuchKey/404, an SFTP no-such-file, a B2 404 — into
// this sentinel, wrapped with the key, so callers can use
// errors.Is(err, ErrNotFound) instead of treating every Get failure (network
// error, permission error, corrupt response, ...) as "the object isn't
// there".
var ErrNotFound = errors.New("object not found")
