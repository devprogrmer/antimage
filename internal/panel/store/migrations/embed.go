// Package migrations embeds the goose migration set so the panel binary
// carries its own schema and needs no files on disk.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
