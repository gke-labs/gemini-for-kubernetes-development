package tasks

import (
	"embed"
)

//go:embed *.sh
var scriptsFS embed.FS
