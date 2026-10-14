package definition

// keySpec is an allowlist node. A nil *keySpec stored as a child means the
// value is a leaf. warn, when set, reports a warning and does not descend.
type keySpec struct {
	children map[string]*keySpec
	elem     *keySpec
	free     bool
	warn     string
}

func leaf() *keySpec { return nil }

var (
	repoSpec = &keySpec{children: map[string]*keySpec{
		"codeForge": leaf(),
		"owner":     leaf(),
		"name":      leaf(),
		"deferred":  leaf(),
	}}

	harnessSpec = &keySpec{children: map[string]*keySpec{
		"type":  leaf(),
		"model": leaf(),
		"auth": {children: map[string]*keySpec{
			"source":     leaf(),
			"secretName": leaf(),
		}},
	}}

	mcpServerSpec = &keySpec{children: map[string]*keySpec{
		"url":       leaf(),
		"headers":   {free: true},
		"env":       {free: true},
		"command":   leaf(),
		"args":      leaf(),
		"warpId":    leaf(),
		"managedId": leaf(),
	}}

	mcpMapSpec = &keySpec{free: true, elem: mcpServerSpec}

	providerSpec = &keySpec{children: map[string]*keySpec{
		"kind":          leaf(),
		"credentialRef": leaf(),
		"region":        leaf(),
		"roleArn":       leaf(),
		"baseUrl":       leaf(),
	}}

	factorySchema = &keySpec{children: map[string]*keySpec{
		"schemaVersion":      leaf(),
		"name":               leaf(),
		"description":        leaf(),
		"alias":              leaf(),
		"codeForges":         leaf(),
		"repositories":       {elem: repoSpec},
		"credentialStrategy": leaf(),
		"integrations": {elem: &keySpec{children: map[string]*keySpec{
			"type": leaf(),
			"slack": {children: map[string]*keySpec{
				"autoRespondToThreadReplies": leaf(),
			}},
			"linear": {free: true},
			"jira":   {free: true},
		}}},
		"secrets":    leaf(),
		"mcpServers": mcpMapSpec,
		"agentDefaults": {children: map[string]*keySpec{
			"model":              leaf(),
			"harness":            harnessSpec,
			"runner":             leaf(),
			"pool":               leaf(),
			"computerUseModel":   leaf(),
			"secrets":            leaf(),
			"mcpServers":         mcpMapSpec,
			"workerHost":         leaf(),
			"credentialStrategy": leaf(),
			"idleTimeoutMinutes": leaf(),
			"maxTurns":           leaf(),
			"required_repos":     {elem: repoSpec},
		}},
		"inference": {children: map[string]*keySpec{
			"default": {children: map[string]*keySpec{
				"provider":      leaf(),
				"credentialRef": leaf(),
			}},
			"providers": {free: true, elem: providerSpec},
		}},
		"pipeline": pipelineSpec,
		"selfImprovement": {children: map[string]*keySpec{
			"failedRunThreshold": leaf(),
			"staleFailureDays":   leaf(),
			"reviewerType":       leaf(),
			"reviewerEmails":     leaf(),
		}},
		"scorerDefaults": {children: map[string]*keySpec{
			"runner":     leaf(),
			"model":      leaf(),
			"secrets":    leaf(),
			"mcpServers": mcpMapSpec,
		}},
		"benchmarkRepoSubstitutions": {elem: &keySpec{children: map[string]*keySpec{
			"source": repoSpec,
			"target": repoSpec,
		}}},
		"cloudProviders": {children: map[string]*keySpec{
			"aws": {children: map[string]*keySpec{
				"roleArn": leaf(),
			}},
			"gcp": {children: map[string]*keySpec{
				"projectNumber":                        leaf(),
				"workloadIdentityFederationPoolId":     leaf(),
				"workloadIdentityFederationProviderId": leaf(),
				"serviceAccountEmail":                  leaf(),
			}},
		}},
	}}

	agentSchema = &keySpec{children: map[string]*keySpec{
		"description":        leaf(),
		"agentType":          leaf(),
		"model":              leaf(),
		"harness":            harnessSpec,
		"spawnableBy":        leaf(),
		"credentialStrategy": leaf(),
		"idleTimeoutMinutes": leaf(),
		"required_repos":     {elem: repoSpec},
		"secrets":            leaf(),
		"mcpServers":         mcpMapSpec,
		"runner":             leaf(),
		"pool":               leaf(),
		"computerUseModel":   leaf(),
		"maxTurns":           leaf(),
		"workerHost":         leaf(),
		"name":               leaf(),
	}}

	runnerSchema = &keySpec{children: map[string]*keySpec{
		"description":   leaf(),
		"setupCommands": leaf(),
		"instanceShape": {children: map[string]*keySpec{
			"vcpus":    leaf(),
			"memoryGb": leaf(),
		}},
		"platform": {children: map[string]*keySpec{
			"os":   leaf(),
			"arch": leaf(),
			"linux": {children: map[string]*keySpec{
				"dockerImage":                  leaf(),
				"registryCredentialSecretName": leaf(),
			}},
			"macos":   {free: true},
			"windows": {free: true},
		}},
		"failureSessionRetentionMinutes": leaf(),
	}}

	filterSpec = newDeepFree()

	pipelineSpec = &keySpec{children: map[string]*keySpec{
		"stages": leaf(),
		"agents": {free: true},
		"skip":   {free: true},
		"approvals": {free: true, elem: &keySpec{children: map[string]*keySpec{
			"required":  leaf(),
			"approvers": leaf(),
		}}},
		"revisionLimit": leaf(),
	}}

	triggerSpec = &keySpec{children: map[string]*keySpec{
		"provider": leaf(),
		"event":    leaf(),
		"filter":   filterSpec,
		"schedule": {children: map[string]*keySpec{
			"name": leaf(),
			"cron": leaf(),
		}},
	}}

	automationSchema = &keySpec{children: map[string]*keySpec{
		"enabled":            leaf(),
		"agent":              leaf(),
		"displayName":        leaf(),
		"triggers":           {elem: triggerSpec},
		"model":              leaf(),
		"harness":            harnessSpec,
		"spawnableBy":        leaf(),
		"credentialStrategy": leaf(),
		"idleTimeoutMinutes": leaf(),
		"required_repos":     {elem: repoSpec},
		"secrets":            leaf(),
		"mcpServers":         mcpMapSpec,
		"runner":             leaf(),
		"pool":               leaf(),
		"computerUseModel":   leaf(),
		"maxTurns":           leaf(),
		"workerHost":         leaf(),
		"description":        leaf(),
	}}

	routerSchema = &keySpec{children: map[string]*keySpec{
		"name":    leaf(),
		"type":    leaf(),
		"default": leaf(),
		"routing": {
			children: map[string]*keySpec{
				"easy":   leaf(),
				"medium": leaf(),
				"hard":   leaf(),
			},
			elem: &keySpec{children: map[string]*keySpec{
				"description": leaf(),
				"model":       leaf(),
			}},
		},
	}}

	scorerAgentSpec = &keySpec{children: map[string]*keySpec{
		"name":               leaf(),
		"includeDescendants": leaf(),
	}}

	scorerSchema = &keySpec{children: map[string]*keySpec{
		"name":        leaf(),
		"description": leaf(),
		"agents":      {elem: scorerAgentSpec},
		"output":      leaf(),
		"labels": {elem: &keySpec{children: map[string]*keySpec{
			"value":       leaf(),
			"description": leaf(),
			"score":       leaf(),
		}}},
		"passingScore":    leaf(),
		"samplingRate":    leaf(),
		"model":           leaf(),
		"harness":         harnessSpec,
		"runner":          leaf(),
		"secrets":         leaf(),
		"mcpServers":      mcpMapSpec,
		"selfImprovement": leaf(),
	}}

	skillSchema = &keySpec{children: map[string]*keySpec{
		"name":        leaf(),
		"description": leaf(),
	}}

	webhookSchema = &keySpec{children: map[string]*keySpec{
		"authMode":         leaf(),
		"signatureScheme":  leaf(),
		"secretName":       leaf(),
		"deliveryIdHeader": leaf(),
		"enabled":          leaf(),
	}}
)

func newDeepFree() *keySpec {
	s := &keySpec{free: true}
	s.elem = s
	return s
}
