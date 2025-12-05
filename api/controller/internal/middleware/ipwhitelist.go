package middleware

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// IPWhitelistMiddleware creates a middleware that only allows requests from whitelisted IPs
// If allowedIPs is empty, all IPs are allowed
func IPWhitelistMiddleware(allowedIPs []string) gin.HandlerFunc {
	// Parse CIDR and single IPs at startup
	var networks []*net.IPNet
	var ips []net.IP

	for _, ipStr := range allowedIPs {
		ipStr = strings.TrimSpace(ipStr)
		if ipStr == "" {
			continue
		}

		if strings.Contains(ipStr, "/") {
			// CIDR format
			_, network, err := net.ParseCIDR(ipStr)
			if err == nil {
				networks = append(networks, network)
			}
		} else {
			// Single IP
			if ip := net.ParseIP(ipStr); ip != nil {
				ips = append(ips, ip)
			}
		}
	}

	return func(ctx *gin.Context) {
		// Empty whitelist means allow all
		if len(allowedIPs) == 0 {
			ctx.Next()
			return
		}

		clientIPStr := ctx.ClientIP()
		clientIP := net.ParseIP(clientIPStr)
		if clientIP == nil {
			ctx.JSON(http.StatusForbidden, gin.H{"error": "invalid client IP"})
			ctx.Abort()
			return
		}

		// Check single IPs
		for _, ip := range ips {
			if ip.Equal(clientIP) {
				ctx.Next()
				return
			}
		}

		// Check CIDR networks
		for _, network := range networks {
			if network.Contains(clientIP) {
				ctx.Next()
				return
			}
		}

		ctx.JSON(http.StatusForbidden, gin.H{"error": "IP not allowed"})
		ctx.Abort()
	}
}
