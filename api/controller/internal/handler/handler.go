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

	// Config management
	configs := v1.Group("/configs")
	{
		configs.GET("/:name", h.GetConfig)
		configs.PUT("/:name", h.UpdateConfig)
		configs.POST("/:name/apply", h.ApplyConfig)
	}

	// Admin whitelist management
	admin := v1.Group("/admin")
	{
		admin.GET("/wallets", h.ListAdminWallets)
		admin.POST("/wallets", h.AddAdminWallet)
		admin.DELETE("/wallets/:address", h.RemoveAdminWallet)

		admin.GET("/ips", h.ListAllowedIPs)
		admin.POST("/ips", h.AddAllowedIP)
		admin.DELETE("/ips/:ip", h.RemoveAllowedIP)
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

// GetConfig returns the config of a container
func (h *Handler) GetConfig(ctx *gin.Context) {
	name := ctx.Param("name")

	content, err := h.ctrl.GetContainerConfig(name)
	if err != nil {
		if _, ok := err.(*ctrl.InvalidContainerError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
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

// UpdateConfig updates the config of a container
func (h *Handler) UpdateConfig(ctx *gin.Context) {
	name := ctx.Param("name")

	var req UpdateConfigRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if err := h.ctrl.UpdateContainerConfig(name, req.Config); err != nil {
		if _, ok := err.(*ctrl.InvalidContainerError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if _, ok := err.(*ctrl.InvalidConfigError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "config updated"})
}

// ApplyConfig updates the config and restarts the container
func (h *Handler) ApplyConfig(ctx *gin.Context) {
	name := ctx.Param("name")

	var req UpdateConfigRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if err := h.ctrl.ApplyContainerConfig(ctx, name, req.Config); err != nil {
		if _, ok := err.(*ctrl.InvalidContainerError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if _, ok := err.(*ctrl.InvalidConfigError); ok {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "config applied and container restarted"})
}

// ListAdminWallets returns all admin wallet addresses
func (h *Handler) ListAdminWallets(ctx *gin.Context) {
	addrs := h.ctrl.GetAdminAddresses()
	ctx.JSON(http.StatusOK, gin.H{"addresses": addrs})
}

// AddWalletRequest is the request body for adding a wallet
type AddWalletRequest struct {
	Address string `json:"address" binding:"required"`
}

// AddAdminWallet adds an admin wallet address
func (h *Handler) AddAdminWallet(ctx *gin.Context) {
	var req AddWalletRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	h.ctrl.AddAdminAddress(req.Address)
	ctx.JSON(http.StatusOK, gin.H{"message": "admin wallet added"})
}

// RemoveAdminWallet removes an admin wallet address
func (h *Handler) RemoveAdminWallet(ctx *gin.Context) {
	address := ctx.Param("address")

	if !h.ctrl.RemoveAdminAddress(address) {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "cannot remove the last admin address"})
		return
	}

	ctx.JSON(http.StatusOK, gin.H{"message": "admin wallet removed"})
}

// ListAllowedIPs returns all allowed IPs
func (h *Handler) ListAllowedIPs(ctx *gin.Context) {
	ips := h.ctrl.GetAllowedIPs()
	ctx.JSON(http.StatusOK, gin.H{"ips": ips})
}

// AddIPRequest is the request body for adding an IP
type AddIPRequest struct {
	IP string `json:"ip" binding:"required"`
}

// AddAllowedIP adds an IP to the whitelist
func (h *Handler) AddAllowedIP(ctx *gin.Context) {
	var req AddIPRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	h.ctrl.AddAllowedIP(req.IP)
	ctx.JSON(http.StatusOK, gin.H{"message": "IP added to whitelist"})
}

// RemoveAllowedIP removes an IP from the whitelist
func (h *Handler) RemoveAllowedIP(ctx *gin.Context) {
	ip := ctx.Param("ip")

	h.ctrl.RemoveAllowedIP(ip)
	ctx.JSON(http.StatusOK, gin.H{"message": "IP removed from whitelist"})
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

// UpdateImages pulls the latest image and updates containers
func (h *Handler) UpdateImages(ctx *gin.Context) {
	result, err := h.ctrl.UpdateImages(ctx)
	if err != nil {
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
