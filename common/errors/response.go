package errors

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func Response(ctx *gin.Context, err error) {
	ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	ctx.Abort()
}
