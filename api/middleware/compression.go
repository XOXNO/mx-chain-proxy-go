package middleware

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	logger "github.com/multiversx/mx-chain-logger-go"
)

var logCompression = logger.GetOrCreate("compression-middleware")

const (
	// compressionLevel defines the gzip compression level (1-9, where 9 is best compression)
	compressionLevel = gzip.BestSpeed // Use BestSpeed for better performance
	// minCompressionSize defines the minimum response size to compress (in bytes)
	minCompressionSize = 1024 // Only compress responses larger than 1KB
)

// CompressionConfig holds configuration for the compression middleware
type CompressionConfig struct {
	// Enabled determines if compression is active
	Enabled bool
	// Level sets the compression level (1-9)
	Level int
	// MinSize sets the minimum response size to compress
	MinSize int
	// Types defines which content types should be compressed
	Types []string
}

// compressionMiddleware implements MiddlewareProcessor for response compression
type compressionMiddleware struct {
	config CompressionConfig
	pool   sync.Pool
}

// NewCompressionMiddleware creates a new compression middleware
func NewCompressionMiddleware(config CompressionConfig) (*compressionMiddleware, error) {
	if config.Level <= 0 || config.Level > 9 {
		config.Level = compressionLevel
	}
	if config.MinSize <= 0 {
		config.MinSize = minCompressionSize
	}
	if len(config.Types) == 0 {
		config.Types = []string{
			"application/json",
			"text/plain",
			"text/html",
			"text/css",
			"text/javascript",
			"application/javascript",
			"application/xml",
			"text/xml",
		}
	}

	cm := &compressionMiddleware{
		config: config,
		pool: sync.Pool{
			New: func() interface{} {
				writer, _ := gzip.NewWriterLevel(io.Discard, config.Level)
				return writer
			},
		},
	}

	return cm, nil
}

// MiddlewareHandlerFunc returns the gin middleware handler function
func (cm *compressionMiddleware) MiddlewareHandlerFunc() gin.HandlerFunc {
	if !cm.config.Enabled {
		return gin.HandlerFunc(func(c *gin.Context) {
			c.Next()
		})
	}

	return gin.HandlerFunc(func(c *gin.Context) {
		// Check if client accepts gzip encoding
		if !cm.shouldCompress(c) {
			c.Next()
			return
		}

		// Get gzip writer from pool
		gzWriter := cm.pool.Get().(*gzip.Writer)
		defer cm.pool.Put(gzWriter)

		// Create compression response writer
		compressionWriter := &compressionResponseWriter{
			ResponseWriter: c.Writer,
			gzWriter:       gzWriter,
			config:         cm.config,
			written:        false,
		}

		// Reset the gzip writer with the actual response writer
		gzWriter.Reset(c.Writer)

		// Replace the response writer
		c.Writer = compressionWriter

		// Set compression headers
		c.Header("Content-Encoding", "gzip")
		c.Header("Vary", "Accept-Encoding")

		// Process the request
		c.Next()

		// Ensure gzip writer is closed
		compressionWriter.Close()
	})
}

// shouldCompress determines if the response should be compressed
func (cm *compressionMiddleware) shouldCompress(c *gin.Context) bool {
	// Check if client accepts gzip
	acceptEncoding := c.GetHeader("Accept-Encoding")
	if !strings.Contains(acceptEncoding, "gzip") {
		return false
	}

	// Don't compress if already encoded
	if c.GetHeader("Content-Encoding") != "" {
		return false
	}

	return true
}


// IsInterfaceNil checks if the interface is nil
func (cm *compressionMiddleware) IsInterfaceNil() bool {
	return cm == nil
}

// compressionResponseWriter wraps gin.ResponseWriter to handle compression
type compressionResponseWriter struct {
	gin.ResponseWriter
	gzWriter *gzip.Writer
	config   CompressionConfig
	written  bool
	size     int
}

// Write implements the io.Writer interface with compression
func (crw *compressionResponseWriter) Write(data []byte) (int, error) {
	// Track total size
	crw.size += len(data)

	// If we haven't written anything yet, check if we should compress
	if !crw.written {
		crw.written = true

		// Check content type
		contentType := crw.Header().Get("Content-Type")
		if contentType == "" {
			contentType = http.DetectContentType(data)
			crw.Header().Set("Content-Type", contentType)
		}

		// Check if content type should be compressed
		if !crw.shouldCompressContentType(contentType) {
			// Don't compress, write directly
			crw.Header().Del("Content-Encoding")
			crw.Header().Del("Vary")
			return crw.ResponseWriter.Write(data)
		}

		// Remove Content-Length header as it will change after compression
		crw.Header().Del("Content-Length")
	}

	// Write to gzip writer
	return crw.gzWriter.Write(data)
}

// WriteHeader writes the status code
func (crw *compressionResponseWriter) WriteHeader(code int) {
	crw.ResponseWriter.WriteHeader(code)
}

// Close closes the gzip writer
func (crw *compressionResponseWriter) Close() error {
	if crw.gzWriter != nil {
		err := crw.gzWriter.Close()
		if err != nil {
			logCompression.Warn("failed to close gzip writer", "error", err)
		}
		return err
	}
	return nil
}

// shouldCompressContentType checks if the content type should be compressed
func (crw *compressionResponseWriter) shouldCompressContentType(contentType string) bool {
	// Check if response is large enough to compress
	if crw.size < crw.config.MinSize {
		return false
	}

	for _, allowedType := range crw.config.Types {
		if strings.Contains(contentType, allowedType) {
			return true
		}
	}
	return false
}

// Status returns the HTTP status code
func (crw *compressionResponseWriter) Status() int {
	return crw.ResponseWriter.Status()
}

// Size returns the size of the response
func (crw *compressionResponseWriter) Size() int {
	return crw.size
}

// Written returns whether the response has been written
func (crw *compressionResponseWriter) Written() bool {
	return crw.written
}

// WriteString writes a string to the response
func (crw *compressionResponseWriter) WriteString(s string) (int, error) {
	return crw.Write([]byte(s))
}

// Hijack implements the http.Hijacker interface
func (crw *compressionResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := crw.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, fmt.Errorf("response writer does not support hijacking")
}

// Flush implements the http.Flusher interface
func (crw *compressionResponseWriter) Flush() {
	// Flush gzip writer first
	if crw.gzWriter != nil {
		err := crw.gzWriter.Flush()
		if err != nil {
			logCompression.Warn("failed to flush gzip writer", "error", err)
		}
	}

	// Flush underlying response writer if it supports flushing
	if flusher, ok := crw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}