package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// ListLogs handles the provider's request to list available log files
// @Summary     List available log files
// @Description Provider can list all available log files using signature authentication
// @Tags        logs
// @Produce     json
// @Param       Address           header string true "Provider's address"
// @Param       Session-Token     header string true "JSON session token with address, provider, timestamp, expiresAt, nonce"
// @Param       Session-Signature header string true "Signature of the session token"
// @Param       component          query  string false "Component name (broker/event), default: both"
// @Success     200 {object} map[string]interface{} "List of log files"
// @Failure     401 {object} map[string]string "Unauthorized"
// @Failure     500 {object} map[string]string "Internal server error"
// @Router      /logs [get]
func (h *Handler) ListLogs(ctx *gin.Context) {
	// Validate provider authentication
	if err := h.ctrl.ValidateProviderAuth(ctx); err != nil {
		handleBrokerError(ctx, errors.NewUnauthorized("%s", err.Error()), "authentication failed")
		return
	}

	// Get component parameter
	component := ctx.Query("component")
	if component == "" {
		component = "both"
	}

	// Validate component parameter
	if component != "broker" && component != "event" && component != "both" {
		handleBrokerError(ctx, errors.New("invalid component parameter, must be 'broker', 'event', or 'both'"), "")
		return
	}

	var allLogFiles []map[string]interface{}

	// List broker logs if requested
	if component == "broker" || component == "both" {
		brokerLogs := h.getComponentLogs("broker")
		allLogFiles = append(allLogFiles, brokerLogs...)
	}

	// List event logs if requested
	if component == "event" || component == "both" {
		eventLogs := h.getComponentLogs("event")
		allLogFiles = append(allLogFiles, eventLogs...)
	}

	// Sort by modified time (newest first)
	sort.Slice(allLogFiles, func(i, j int) bool {
		return allLogFiles[i]["modifiedTime"].(int64) > allLogFiles[j]["modifiedTime"].(int64)
	})

	ctx.JSON(200, gin.H{
		"logs": allLogFiles,
	})
}

// getComponentLogs returns log files for a specific component
func (h *Handler) getComponentLogs(component string) []map[string]interface{} {
	logDir := h.ctrl.GetComponentLogDir(component)
	if logDir == "" {
		return nil
	}

	// List all files in the directory
	files, err := os.ReadDir(logDir)
	if err != nil {
		return nil
	}

	// Default log file name
	baseName := "inference.log"

	var logFiles []map[string]interface{}
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		fileName := file.Name()
		// Check if it's a log file (main log or rotated log)
		if fileName == baseName || strings.HasPrefix(fileName, baseName+".") {
			info, err := file.Info()
			if err != nil {
				continue
			}

			logFiles = append(logFiles, map[string]interface{}{
				"name":         fileName,
				"size":         info.Size(),
				"modifiedTime": info.ModTime().Unix(),
				"isCurrentLog": fileName == baseName,
				"component":    component,
			})
		}
	}

	return logFiles
}

// GetLogFile handles the provider's request to download a specific log file
// @Summary     Download a specific log file
// @Description Provider can download a specific log file using signature authentication
// @Tags        logs
// @Produce     application/octet-stream
// @Param       Address           header string true "Provider's address"
// @Param       Session-Token     header string true "JSON session token with address, provider, timestamp, expiresAt, nonce"
// @Param       Session-Signature header string true "Signature of the session token"
// @Param       component          path   string true "Component name (broker/event)"
// @Param       filename           path   string true "Log file name"
// @Success     200 {file} binary "Log file content"
// @Failure     400 {object} map[string]string "Invalid component or filename"
// @Failure     401 {object} map[string]string "Unauthorized"
// @Failure     404 {object} map[string]string "Log file not found"
// @Failure     500 {object} map[string]string "Internal server error"
// @Router      /logs/{component}/{filename} [get]
func (h *Handler) GetLogFile(ctx *gin.Context) {
	// Validate provider authentication
	if err := h.ctrl.ValidateProviderAuth(ctx); err != nil {
		handleBrokerError(ctx, errors.NewUnauthorized("%s", err.Error()), "authentication failed")
		return
	}

	// Get component and filename from path
	component := ctx.Param("component")
	filename := ctx.Param("filename")

	if component == "" {
		handleBrokerError(ctx, errors.New("component is required"), "")
		return
	}

	if filename == "" {
		handleBrokerError(ctx, errors.New("filename is required"), "")
		return
	}

	// Validate component
	if component != "broker" && component != "event" {
		handleBrokerError(ctx, errors.New("invalid component, must be 'broker' or 'event'"), "")
		return
	}

	// Prevent path traversal attacks
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		handleBrokerError(ctx, errors.New("invalid filename"), "")
		return
	}

	// Get log directory for the component
	logDir := h.ctrl.GetComponentLogDir(component)
	if logDir == "" {
		handleBrokerError(ctx, errors.NewInternal("log directory not configured for component"), "")
		return
	}

	// Default log file name
	baseName := "inference.log"

	// Validate that the requested file is a valid log file
	if filename != baseName && !strings.HasPrefix(filename, baseName+".") {
		handleBrokerError(ctx, errors.New("invalid log file"), "")
		return
	}

	// Construct full file path
	fullPath := filepath.Join(logDir, filename)

	// Check if file exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		handleBrokerError(ctx, errors.NewNotFound("log file not found"), "")
		return
	}

	// Set appropriate headers for file download
	ctx.Header("Content-Description", "File Transfer")
	ctx.Header("Content-Transfer-Encoding", "binary")
	ctx.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s-%s\"", component, filename))
	ctx.Header("Content-Type", "application/octet-stream")

	// Stream the log file to the client
	ctx.File(fullPath)
}