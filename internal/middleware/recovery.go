package middleware

import (
	"errors"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Recovery returns a middleware that recovers from panics.
//
// It replaces gin.RecoveryWithWriter on purpose: gin's recovery dumps the raw
// request (request line with the query string and every header except
// Authorization) into the log in debug mode and on broken connections, which
// leaks the citizen's phone number (?user_number=...) and other personal data
// (LGPD). This middleware logs only the method, the path without the query,
// the request ID, the client IP, the panic value and the stack trace.
func Recovery(logger *logrus.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			// A deliberate abort (as net/http defines it) is not an error:
			// let the server close the connection without logging.
			if recovered == http.ErrAbortHandler {
				panic(recovered)
			}

			brokenPipe := isBrokenPipe(recovered)
			requestID, _ := c.Get(RequestIDKey)

			fields := logrus.Fields{
				"request_id": requestID,
				"panic":      recovered,
				"method":     c.Request.Method,
				"path":       c.Request.URL.Path,
				"client_ip":  c.ClientIP(),
			}

			if brokenPipe {
				// The client went away: not a server fault, no stack trace,
				// and the connection is dead, so no status can be written.
				fields["broken_pipe"] = true
				logger.WithFields(fields).Error("Panic recovered")
				if err, ok := recovered.(error); ok {
					_ = c.Error(err)
				}
				c.Abort()
				return
			}

			fields["stack"] = string(debug.Stack())
			logger.WithFields(fields).Error("Panic recovered")

			c.AbortWithStatus(http.StatusInternalServerError)
		}()

		c.Next()
	}
}

// isBrokenPipe reports whether a recovered panic value is a broken connection
// (broken pipe or connection reset by peer), using the same check as gin.
func isBrokenPipe(recovered any) bool {
	opErr, ok := recovered.(*net.OpError)
	if !ok {
		return false
	}

	var syscallErr *os.SyscallError
	if !errors.As(opErr, &syscallErr) {
		return false
	}

	msg := strings.ToLower(syscallErr.Error())
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer")
}
