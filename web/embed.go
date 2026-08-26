package web

import "embed"

//go:embed dist
var content embed.FS

func FS() embed.FS { return content }
