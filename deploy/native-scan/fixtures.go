package nativescan

import "embed"

//go:embed fixtures/definition.json fixtures/sample-task.yaml compose.yaml
var embedded embed.FS
