package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/controller/internal/ctrl"
)

// Handler handles HTTP requests for the controller
type Handler struct {
	ctrl *ctrl.Ctrl
}

// NewHandler creates a new handler
func NewHandler(c *ctrl.Ctrl) *Handler {
	return &Handler{ctrl: c}
}

// RegisterRoutes registers all routes on the given router group
func (h *Handler) RegisterRoutes(v1 *gin.RouterGroup) {
	// Container management
	containers := v1.Group("/containers")
	{
		containers.GET("", h.ListContainers)
		containers.GET("/:name", h.GetContainer)
		containers.POST("/:name/start", h.StartContainer)
		containers.POST("/:name/stop", h.StopContainer)
		containers.POST("/:name/restart", h.RestartContainer)
	}

	// Config management - organized by function
	config := v1.Group("/config")
	{
		// Core config (shared by broker and event) - YAML file
		// PUT updates config AND restarts broker+event
		config.GET("/core", h.GetCoreConfig)
		config.PUT("/core", h.UpdateCoreConfig)

		// Ingress config - environment variables
		config.GET("/ingress", h.GetIngressConfig)
		config.PUT("/ingress", h.UpdateIngressConfig)

		// Prometheus config - base64 encoded YAML
		config.GET("/prometheus", h.GetPrometheusConfig)
		config.PUT("/prometheus", h.UpdatePrometheusConfig)
	}

	// Admin whitelist inspection. Read-only by design: AuthMiddleware gates this
	// whole group on the wallet list, so it is fixed at startup rather than
	// editable through the routes it guards.
	admin := v1.Group("/admin")
	{
		admin.GET("/wallets", h.ListAdminWallets)
		admin.GET("/ips", h.ListAllowedIPs)
	}

	// Image management
	images := v1.Group("/images")
	{
		images.GET("/info", h.GetImageInfo)
		images.POST("/update", h.UpdateImages)
	}
}

// ListContainers returns the status of all managed containers
func (h *Handler) ListContainers(ctx *gin.Context) {
	statuses, err := h.ctrl.GetAllContainersStatus(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"containers": statuses})
}

// GetContainer returns the status of a specific container
func (h *Handler) GetContainer(ctx *gin.Context) {
	name := ctx.Param("name")

	status, err := h.ctrl.GetContainerStatus(ctx, name)
	if err != nil {
		if _, ok := err.(*ctrl.InvalidContainerError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if status == nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "container not found"})
		return
	}

	ctx.JSON(http.StatusOK, status)
}

// StartContainer starts a container
func (h *Handler) StartContainer(ctx *gin.Context) {
	name := ctx.Param("name")

	if err := h.ctrl.StartContainer(ctx, name); err != nil {
		if _, ok := err.(*ctrl.InvalidContainerError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "container started"})
}

// StopContainer stops a container
func (h *Handler) StopContainer(ctx *gin.Context) {
	name := ctx.Param("name")

	if err := h.ctrl.StopContainer(ctx, name); err != nil {
		if _, ok := err.(*ctrl.InvalidContainerError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "container stopped"})
}

// RestartContainer restarts a container
func (h *Handler) RestartContainer(ctx *gin.Context) {
	name := ctx.Param("name")

	if err := h.ctrl.RestartContainer(ctx, name); err != nil {
		if _, ok := err.(*ctrl.InvalidContainerError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "container restarted"})
}

// GetCoreConfig returns the core config (shared by broker and event)
func (h *Handler) GetCoreConfig(ctx *gin.Context) {
	content, err := h.ctrl.GetCoreConfig()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"config": content})
}

// UpdateConfigRequest is the request body for updating config
// Config is the raw YAML string content to avoid parsing issues with hex addresses
type UpdateConfigRequest struct {
	Config string `json:"config" binding:"required"`
}

// UpdateCoreConfig updates the core config and restarts broker+event containers
func (h *Handler) UpdateCoreConfig(ctx *gin.Context) {
	var req UpdateConfigRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if err := h.ctrl.ApplyCoreConfig(ctx, req.Config); err != nil {
		if _, ok := err.(*ctrl.InvalidConfigError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "config updated and containers restarted"})
}

// ListAdminWallets returns all admin wallet addresses
func (h *Handler) ListAdminWallets(ctx *gin.Context) {
	addrs := h.ctrl.GetAdminAddresses()
	ctx.JSON(http.StatusOK, gin.H{"addresses": addrs})
}

// ListAllowedIPs returns all allowed IPs
func (h *Handler) ListAllowedIPs(ctx *gin.Context) {
	ips := h.ctrl.GetAllowedIPs()
	ctx.JSON(http.StatusOK, gin.H{"ips": ips})
}

// GetImageInfo returns information about the current image
func (h *Handler) GetImageInfo(ctx *gin.Context) {
	info, err := h.ctrl.GetImageInfo(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, info)
}

// UpdateImagesRequest is the request body for an image upgrade.
//
// The digest is required and is the whole of what the caller gets to choose:
// the repository comes from controller.imageRepo, so there is no request shape
// that pulls by tag.
type UpdateImagesRequest struct {
	Digest string `json:"digest" binding:"required"` // "sha256:" followed by 64 lowercase hex characters
}

// UpdateImages recreates the containers on the image named by the given digest
func (h *Handler) UpdateImages(ctx *gin.Context) {
	var req UpdateImagesRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "digest field is required"})
		return
	}

	result, err := h.ctrl.UpdateImages(ctx, req.Digest)
	if err != nil {
		if _, ok := err.(*ctrl.InvalidDigestError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// Return the result even on error, as it contains partial progress info
		if result != nil {
			ctx.JSON(http.StatusInternalServerError, result)
		} else {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	ctx.JSON(http.StatusOK, result)
}

// GetIngressConfig returns the current ingress environment variables
func (h *Handler) GetIngressConfig(ctx *gin.Context) {
	env, err := h.ctrl.GetIngressEnv(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"env": env})
}

// UpdateIngressConfigRequest is the request body for updating ingress configuration
type UpdateIngressConfigRequest struct {
	Env map[string]string `json:"env" binding:"required"`
}

// UpdateIngressConfig updates the ingress configuration
func (h *Handler) UpdateIngressConfig(ctx *gin.Context) {
	var req UpdateIngressConfigRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "env field is required"})
		return
	}

	if err := h.ctrl.UpdateIngressConfig(ctx, req.Env); err != nil {
		if _, ok := err.(*ctrl.ForbiddenEnvKeyError); ok {
			ctx.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "ingress config updated"})
}

// GetPrometheusConfig returns the current Prometheus configuration
func (h *Handler) GetPrometheusConfig(ctx *gin.Context) {
	config, err := h.ctrl.GetPrometheusConfig(ctx)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"config": config})
}

// UpdatePrometheusConfigRequest is the request body for updating Prometheus config
type UpdatePrometheusConfigRequest struct {
	Config string `json:"config" binding:"required"` // base64 encoded prometheus.yml
}

// UpdatePrometheusConfig updates the Prometheus configuration
func (h *Handler) UpdatePrometheusConfig(ctx *gin.Context) {
	var req UpdatePrometheusConfigRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "config field is required"})
		return
	}

	if err := h.ctrl.UpdatePrometheusConfig(ctx, req.Config); err != nil {
		if _, ok := err.(*ctrl.InvalidConfigError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "prometheus config updated and applied"})
}
