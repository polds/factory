package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeTreeRequestParsesPublishAndEvidence(t *testing.T) {
	body := `{
		"personas": [{"name": "lead", "reportsTo": "", "spawnableBy": [], "role": "lead"}],
		"maxDepth": 3,
		"absoluteMaxDepth": 8,
		"publish": {"issueKey": "widget#1", "title": "draft", "body": "text", "refPrefix": "factory/draft-"},
		"evidence": {"package": "./oracle", "mutation": "oracle/oracle.go"}
	}`
	var req NativeTreeRequest
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		t.Fatal(err)
	}
	if req.Publish == nil || req.Publish.IssueKey != "widget#1" || req.Publish.RefPrefix != "factory/draft-" || req.Evidence == nil || req.Evidence.Package != "./oracle" || req.Evidence.Mutation != "oracle/oracle.go" {
		t.Fatalf("%+v", req)
	}
	for _, extra := range []string{
		`{"repo":"example/widget"}`,
		`{"rolePath":"team/roles/{slug}/{file}"}`,
		`{"sentinel":"factoryEvidencePass"}`,
		`{"price":{"models":[]}}`,
		`{"publish":{"repo":"example/widget"}}`,
		`{"evidence":{"sentinel":"factoryEvidencePass"}}`,
	} {
		dec := json.NewDecoder(strings.NewReader(extra))
		dec.DisallowUnknownFields()
		var rejected NativeTreeRequest
		if err := dec.Decode(&rejected); err == nil {
			t.Fatalf("accepted %s", extra)
		}
	}
}
