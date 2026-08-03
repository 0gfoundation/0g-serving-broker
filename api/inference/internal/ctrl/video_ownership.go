package ctrl

import (
	"strings"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// truncateAddr shortens an Ethereum address for logging (first 6 + last 4 characters),
// following the CLAUDE.md guidance to avoid logging full addresses — mirrors
// internal/proxy's own truncateAddr (unexported there too, and proxy already imports ctrl, so
// this package can't reuse that copy without a cyclic import). Short or empty inputs are
// returned unchanged.
func truncateAddr(addr string) string {
	if len(addr) <= 12 {
		return addr
	}
	return addr[:6] + "…" + addr[len(addr)-4:]
}

// isDuplicateKeyError reports whether err looks like a MySQL/GORM unique-constraint violation
// (MySQL error 1062, "Duplicate entry ... for key ..."). GORM's TranslateError option (which
// would surface a typed gorm.ErrDuplicatedKey) is not enabled for this DB connection, so this
// falls back to matching the driver's raw error text — the same convention this codebase's own
// DB-layer tests already use (e.g. `strings.Contains(err.Error(), "Duplicate")`). Used only to
// choose a log message, not for any control-flow decision, so a false negative here just means
// a less specific (still accurate) log line, not a behavior change.
func isDuplicateKeyError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Duplicate entry")
}

// AuthorizeVideoJobAccess verifies that userAddress is the address that created the
// video-generation job identified by providerJobID — gating GET /videos/{id} and
// GET /videos/{id}/content (proxy.go's AuthRequiredPrefixes passthrough), which otherwise
// forward to the provider once the caller has ANY valid broker session, regardless of whether
// they created this particular job. See model.VideoJobOwner and issue #591.
//
// Fails closed: a lookup miss (an unknown job id, a job created before ownership tracking
// existed, or a transient DB error) is treated as not authorized, the same as a recorded owner
// that doesn't match — deliberately not distinguished for the caller, since "unknown" and
// "wrong owner" both mean the same thing here: this session may not access this job.
func (c *Ctrl) AuthorizeVideoJobAccess(providerJobID, userAddress string) error {
	owner, err := c.videoJobOwnerDB.GetVideoJobOwner(providerJobID)
	if err != nil {
		c.logger.Warnf("video job access denied for job %s: no recorded owner (%v), caller=%s", providerJobID, err, truncateAddr(userAddress))
		return errors.NewForbidden("you do not have permission to access this video job")
	}
	if !strings.EqualFold(owner.UserAddress, userAddress) {
		c.logger.Warnf("video job access denied for job %s: caller=%s does not match the recorded owner", providerJobID, truncateAddr(userAddress))
		return errors.NewForbidden("you do not have permission to access this video job")
	}
	return nil
}

// VideoJobChatKey returns the ZG-Res-Key handle to replay on a video STATUS
// response, or "" when there is none to replay. Never for /videos/{id}/content:
// the signature binds the terminal poll JSON, not the mp4 bytes.
//
// The handle is minted and advertised once, on the create response
// (handleVideoGenerationResponse). Video status and content are
// AuthRequiredPrefixes passthroughs, and that path returns from
// ProcessHTTPRequest before any signing or header work — so a client that did not
// capture the header from the create response had no way to obtain it again, and
// the signature it points at was unreachable for the whole life of the job. Async
// image never had this gap: its status handler restores ZG-Res-Key from the stored
// response headers, gated on the job being completed.
//
// Errors are swallowed to "" on purpose. This is a best-effort convenience on a
// path whose job is to return the customer's video: a DB blip must not fail the
// poll, and the caller degrades to exactly the pre-existing behaviour (no header,
// so the router falls back as it does today).
func (c *Ctrl) VideoJobChatKey(providerJobID string) string {
	chatKey, err := c.videoPollDB.GetVideoPollJobChatKey(providerJobID)
	if err != nil {
		c.logger.Warnf("video job %s: could not read the signature handle to replay: %v", providerJobID, err)
		return ""
	}
	return chatKey
}

// VideoJobChatKeyForTest exposes the raw lookup so an integration test can wait for
// the poll scheduler to resolve a job. The replay is gated on the ROW's status, so
// polling the API cannot tell a test when that gate has opened.
func (c *Ctrl) VideoJobChatKeyForTest(providerJobID string) (string, error) {
	return c.videoPollDB.GetVideoPollJobChatKey(providerJobID)
}
