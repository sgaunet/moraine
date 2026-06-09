// Package web holds the embedded static assets (HTML/CSS/JS) for the moraine UI.
// Embedding keeps moraine a single self-contained binary (Constitution Principle I).
package web

import "embed"

// FS contains the UI assets served by the HTTP server.
//
//go:embed index.html style.css app.js placeholder.svg
var FS embed.FS
