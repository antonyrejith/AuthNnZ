package openbao

import (
	"context"
	"net/http"
	"time"

	"github.com/antonyrejith/portun/internal/platform/errs"
)

// claimSuffix names the claim key of a key name: "<name>.created". Names of
// real keys cannot contain '.', so a claim never collides with a key.
const claimSuffix = ".created"

const (
	// claimVersion is the version a claim key reaches with the winning
	// rotation.
	claimVersion = 2
	// claimAttempts bounds the rotations of a claim that fail without
	// another caller having won.
	claimAttempts   = 5
	claimRetryDelay = 20 * time.Millisecond
)

// claim makes CreateKey exclusive. Transit's key creation is an upsert that
// answers identically to every caller, and its import endpoint checks for an
// existing key outside its lock, so neither can tell concurrent creators
// apart. Rotation, however, runs under the key's exclusive lock and returns
// the new latest version, so every rotation of a key gets a distinct
// version. CreateKey therefore creates (upserts) the name's claim key and
// rotates it once: the caller whose rotation produced version 2 created the
// key; every other caller gets CodeConflict. The claim key holds no data and
// stays, like the tombstone. A rotation that fails because it raced with
// another (OpenBao's storage transactions reject one of them) is retried
// until the claim's version shows a winner.
func (k *KMS) claim(ctx context.Context, name string) error {
	path := "keys/" + name + claimSuffix
	body := map[string]any{"type": typeEncrypt, "exportable": false, "allow_plaintext_backup": false}
	if err := k.call(ctx, http.MethodPost, path, body, nil); err != nil {
		return statusError(err, "create key claim")
	}
	var out keyResponse
	for attempt := 1; ; attempt++ {
		err := k.call(ctx, http.MethodPost, path+"/rotate", nil, &out)
		if err == nil {
			break
		}
		// OpenBao's transactional storage fails a rotation that raced with
		// another one. If the claim has moved past version 1, another caller
		// won; otherwise retry a few times.
		var cur keyResponse
		if k.call(ctx, http.MethodGet, path, nil, &cur) == nil && cur.Data.LatestVersion >= claimVersion {
			return errs.New(errs.CodeConflict, "key already exists (created concurrently)")
		}
		if attempt == claimAttempts {
			return statusError(err, "rotate key claim")
		}
		select {
		case <-ctx.Done():
			return wrap(ctx.Err())
		case <-time.After(time.Duration(attempt) * claimRetryDelay):
		}
	}
	switch v := out.Data.LatestVersion; {
	case v == claimVersion:
		return nil
	case v > claimVersion:
		return errs.New(errs.CodeConflict, "key already exists (created concurrently)")
	default:
		// No version in the answer: the claim cannot be decided, so fail
		// closed rather than let two callers believe they created the key.
		return errs.New(errs.CodeInternal, "OpenBao did not report the claim key's version")
	}
}
