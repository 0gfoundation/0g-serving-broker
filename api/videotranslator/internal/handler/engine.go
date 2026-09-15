package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/translatorhttp"
)

// NewEngine builds this sidecar's gin engine with the upstream-TLS report
// installed.
//
// It delegates to common/translatorhttp, which is shared with the audio
// translator. The middleware lived here until a second translator needed it and
// Go's internal-package rule made it unreachable — duplicating it would have put
// the broker's routing-proof evidence in two places that could drift, which is
// the opposite of why it is middleware at all.
func NewEngine() *gin.Engine {
	return translatorhttp.NewEngine()
}
