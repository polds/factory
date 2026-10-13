package harness

import (
	"encoding/json"
	"os"
)

// TaskFile is /var/run/factory/task.json. The unmodified worker does not
// create this file. The control plane, or a test, writes it. The prompt may
// also arrive as FACTORY_TASK_PROMPT, which the worker will forward because
// it copies assignment env_vars into the task process.
type TaskFile struct {
	Prompt         string   `json:"prompt,omitempty"`
	ModelID        string   `json:"model_id,omitempty"`
	Secrets        []string `json:"secrets,omitempty"`
	SetupCommand   string   `json:"setup_command,omitempty"`
	HarnessAuthEnv []string `json:"harness_auth_env,omitempty"`
}

func LoadTaskFile(path string) (TaskFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return TaskFile{}, nil
		}
		return TaskFile{}, err
	}
	var tf TaskFile
	if err := json.Unmarshal(b, &tf); err != nil {
		return TaskFile{}, err
	}
	return tf, nil
}
