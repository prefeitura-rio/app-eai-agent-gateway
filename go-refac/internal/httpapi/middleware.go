package httpapi

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

// RequestLogger registra latência e status das requisições para facilitar debug/observabilidade.
func RequestLogger() gin.HandlerFunc {
    return func(c *gin.Context) {
        start := time.Now()
        c.Next()
        dur := time.Since(start)
        log.Info().
            Str("method", c.Request.Method).
            Str("path", c.FullPath()).
            Int("status", c.Writer.Status()).
            Dur("latency", dur).
            Msg("http")
    }
}


