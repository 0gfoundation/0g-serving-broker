package middleware

import (
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimitInfo holds rate limit state for a single dimension (RPM, TPM, or IPM).
type RateLimitInfo struct {
	Limit     int     // Configured limit (e.g., 30 RPM or 60000 TPM)
	Remaining int     // Remaining budget in this window
	ResetSecs float64 // Seconds until the limit resets/refills
}

// SetRateLimitHeaders sets rate limit response headers in the format appropriate
// for the endpoint: OpenAI format for /chat/completions, Anthropic format for /messages.
// resourceType is the name of the resource dimension: "tokens" or "images".
// If empty, defaults to "tokens" for backward compatibility.
func SetRateLimitHeaders(c *gin.Context, path string, rpm *RateLimitInfo, resource *RateLimitInfo, resourceType string) {
	if resourceType == "" {
		resourceType = "tokens"
	}

	if IsAnthropicEndpoint(path) {
		setAnthropicHeaders(c, rpm, resource, resourceType)
	} else {
		setOpenAIHeaders(c, rpm, resource, resourceType)
	}

	// Expose rate limit headers via CORS
	exposeRateLimitHeaders(c, path, resourceType)
}

func setOpenAIHeaders(c *gin.Context, rpm *RateLimitInfo, resource *RateLimitInfo, resourceType string) {
	if rpm != nil {
		c.Writer.Header().Set("x-ratelimit-limit-requests", fmt.Sprintf("%d", rpm.Limit))
		c.Writer.Header().Set("x-ratelimit-remaining-requests", fmt.Sprintf("%d", rpm.Remaining))
		c.Writer.Header().Set("x-ratelimit-reset-requests", fmt.Sprintf("%.0fs", rpm.ResetSecs))
	}
	if resource != nil {
		c.Writer.Header().Set(fmt.Sprintf("x-ratelimit-limit-%s", resourceType), fmt.Sprintf("%d", resource.Limit))
		c.Writer.Header().Set(fmt.Sprintf("x-ratelimit-remaining-%s", resourceType), fmt.Sprintf("%d", resource.Remaining))
		c.Writer.Header().Set(fmt.Sprintf("x-ratelimit-reset-%s", resourceType), fmt.Sprintf("%.0fs", resource.ResetSecs))
	}
}

func setAnthropicHeaders(c *gin.Context, rpm *RateLimitInfo, resource *RateLimitInfo, resourceType string) {
	if rpm != nil {
		resetTime := time.Now().Add(time.Duration(rpm.ResetSecs * float64(time.Second))).UTC().Format(time.RFC3339)
		c.Writer.Header().Set("anthropic-ratelimit-requests-limit", fmt.Sprintf("%d", rpm.Limit))
		c.Writer.Header().Set("anthropic-ratelimit-requests-remaining", fmt.Sprintf("%d", rpm.Remaining))
		c.Writer.Header().Set("anthropic-ratelimit-requests-reset", resetTime)
	}
	if resource != nil {
		resetTime := time.Now().Add(time.Duration(resource.ResetSecs * float64(time.Second))).UTC().Format(time.RFC3339)
		c.Writer.Header().Set(fmt.Sprintf("anthropic-ratelimit-%s-limit", resourceType), fmt.Sprintf("%d", resource.Limit))
		c.Writer.Header().Set(fmt.Sprintf("anthropic-ratelimit-%s-remaining", resourceType), fmt.Sprintf("%d", resource.Remaining))
		c.Writer.Header().Set(fmt.Sprintf("anthropic-ratelimit-%s-reset", resourceType), resetTime)
	}
}

func exposeRateLimitHeaders(c *gin.Context, path string, resourceType string) {
	var headers []string
	if IsAnthropicEndpoint(path) {
		headers = []string{
			"anthropic-ratelimit-requests-limit",
			"anthropic-ratelimit-requests-remaining",
			"anthropic-ratelimit-requests-reset",
			fmt.Sprintf("anthropic-ratelimit-%s-limit", resourceType),
			fmt.Sprintf("anthropic-ratelimit-%s-remaining", resourceType),
			fmt.Sprintf("anthropic-ratelimit-%s-reset", resourceType),
		}
	} else {
		headers = []string{
			"x-ratelimit-limit-requests",
			"x-ratelimit-remaining-requests",
			"x-ratelimit-reset-requests",
			fmt.Sprintf("x-ratelimit-limit-%s", resourceType),
			fmt.Sprintf("x-ratelimit-remaining-%s", resourceType),
			fmt.Sprintf("x-ratelimit-reset-%s", resourceType),
		}
	}

	existing := c.Writer.Header().Get("Access-Control-Expose-Headers")
	if existing != "" {
		headers = append([]string{existing}, headers...)
	}
	c.Writer.Header().Set("Access-Control-Expose-Headers", strings.Join(headers, ", "))
}
