// Package pomona carries the front end into the binary.
//
// The server and the Chrome extension show the same pages: the brief, the
// settings, the pairing screen. Embedding them here rather than copying them
// means the two can't drift, and a built server needs no files beside it.
package pomona

import "embed"

//go:embed src icons
var Files embed.FS
