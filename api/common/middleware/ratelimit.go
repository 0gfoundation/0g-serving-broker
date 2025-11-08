package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"github.com/gin-gonic/gin"
)

type RateLimiter struct {
	visitors map[string]*rate.Limiter
	mu       sync.RWMutex
	rate     rate.Limit
	burst    int
}

func NewRateLimiter(r rate.Limit, burst int) *RateLimiter {
	rl := &RateLimiter{
		visitors: make(map[string]*rate.Limiter),
		rate:     r,
		burst:    burst,
	}
	
	go rl.cleanupVisitors()
	
	return rl
}

func (rl *RateLimiter) cleanupVisitors() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	
	for {
		<-ticker.C
		rl.mu.Lock()
		for ip, limiter := range rl.visitors {
			if limiter.Tokens() == float64(rl.burst) {
				delete(rl.visitors, ip)
			}
		}
		rl.mu.Unlock()
	}
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	
	limiter, exists := rl.visitors[ip]
	if !exists {
		limiter = rate.NewLimiter(rl.rate, rl.burst)
		rl.visitors[ip] = limiter
	}
	
	return limiter
}

func (rl *RateLimiter) Allow(ip string) bool {
	limiter := rl.getLimiter(ip)
	return limiter.Allow()
}

func RateLimitMiddleware(limiter *RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := getClientIP(c)
		
		if !limiter.Allow(ip) {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": "Too many requests. Please try again later.",
			})
			c.Abort()
			return
		}
		
		c.Next()
	}
}

func getClientIP(c *gin.Context) string {
	// Try to get the real IP from common headers first (only when behind proxy)
	// Note: These headers are typically set by reverse proxies, not clients
	if xForwardedFor := c.Request.Header.Get("X-Forwarded-For"); xForwardedFor != "" {
		// X-Forwarded-For may contain multiple IPs, get the first one (original client)
		ips := strings.Split(xForwardedFor, ",")
		if len(ips) > 0 {
			ip := strings.TrimSpace(ips[0])
			// Basic validation to ensure it's not empty and looks like an IP
			if ip != "" && !strings.Contains(ip, " ") {
				return ip
			}
		}
	}
	
	if xRealIP := c.Request.Header.Get("X-Real-IP"); xRealIP != "" {
		// Validate the IP is not empty
		if strings.TrimSpace(xRealIP) != "" {
			return strings.TrimSpace(xRealIP)
		}
	}
	
	// Gin's ClientIP method provides built-in logic for IP detection
	// This handles RemoteAddr and validates the result
	if clientIP := c.ClientIP(); clientIP != "" {
		return clientIP
	}
	
	// Final fallback to RemoteAddr (should rarely be needed)
	remoteAddr := c.Request.RemoteAddr
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		return remoteAddr[:idx]
	}
	return remoteAddr
}