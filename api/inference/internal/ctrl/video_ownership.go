package ctrl

import (
	"strings"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

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
		c.logger.Warnf("video job access denied for job %s: no recorded owner (%v), caller=%s", providerJobID, err, userAddress)
		return errors.NewForbidden("you do not have permission to access this video job")
	}
	if !strings.EqualFold(owner.UserAddress, userAddress) {
		c.logger.Warnf("video job access denied for job %s: caller=%s does not match the recorded owner", providerJobID, userAddress)
		return errors.NewForbidden("you do not have permission to access this video job")
	}
	return nil
}
