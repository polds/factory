// Command claude is a credential-free Claude Code stand-in.
// It prints the pinned help text and NDJSON fixtures. It does not read
// API keys and it does not call a model.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	fmt.Fprintf(os.Stderr, "argv:%s\n", strings.Join(os.Args[1:], " "))
	if has(os.Args[1:], "--help") {
		if _, err := fmt.Fprint(os.Stdout, helpText); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	scenario := flagValue(os.Args[1:], "--scenario")
	if scenario == "" {
		scenario = "empty-text"
	}
	if scenario == "hold" {
		time.Sleep(time.Hour)
		return
	}
	body, ok := scenarios[scenario]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown scenario %s\n", scenario)
		os.Exit(2)
	}
	if _, err := fmt.Fprint(os.Stdout, body); err != nil {
		os.Exit(1)
	}
}

func has(args []string, name string) bool {
	for _, arg := range args {
		if arg == name {
			return true
		}
	}
	return false
}

func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

const helpText = `Claude Code fixture (credential-free pin)

Usage:
  claude [options]

Options:
      --help
      --print
      --output-format stream-json
      --resume string
      --model string
      --max-turns int
      --permission-mode string
`

var scenarios = map[string]string{
	"empty-text":    "{\"type\":\"assistant\",\"text\":\"\",\"call_id\":\"c-empty\"}\n",
	"empty-tool":    "{\"type\":\"tool_result\",\"tool\":\"bash\",\"output\":\"\",\"ok\":true,\"call_id\":\"c-tool\"}\n",
	"refusal":       "{\"type\":\"refusal\",\"text\":\"refused\",\"call_id\":\"c-refuse\"}\n",
	"tool-error":    "{\"type\":\"tool_result\",\"tool\":\"bash\",\"ok\":false,\"error\":\"boom\",\"call_id\":\"c-terr\"}\n",
	"malformed":     "{\"type\":\"assistant\",\"text\":\"before\",\"call_id\":\"c-ok\"}\n{not-json\n",
	"partial":       "{\"type\":\"assistant\",\"text\":\"half\",\"partial\":true,\"call_id\":\"c-part\"}\n",
	"http-401":      "{\"type\":\"error\",\"status\":401,\"call_id\":\"c-401\"}\n",
	"http-400":      "{\"type\":\"error\",\"status\":400,\"call_id\":\"c-400\"}\n",
	"http-429":      "{\"type\":\"error\",\"status\":429,\"call_id\":\"c-429\"}\n",
	"timeout":       "{\"type\":\"error\",\"status\":408,\"error\":\"timeout\",\"call_id\":\"c-time\"}\n",
	"delayed-usage": "{\"type\":\"assistant\",\"text\":\"done\",\"call_id\":\"c-use\"}\n{\"type\":\"usage\",\"call_id\":\"c-use\",\"usage\":{\"input_tokens\":3,\"output_tokens\":4}}\n",
	"resume":        "{\"type\":\"resume\",\"session_id\":\"s2\",\"resume_of\":\"s1\",\"call_id\":\"c-res\"}\n{\"type\":\"assistant\",\"text\":\"continued\",\"call_id\":\"c-res\"}\n",
	"duplicate":     "{\"type\":\"assistant\",\"text\":\"once\",\"call_id\":\"c-dup\"}\n{\"type\":\"assistant\",\"text\":\"again\",\"call_id\":\"c-dup\"}\n",
}
