package schema

import (
	"strings"
	"testing"
)

func TestFactorySchemaSlackLeaves(t *testing.T) {
	schema := readSchema(t, "factory.schema.json")
	base := func(slack string) string {
		return `{
			"schemaVersion": "v1alpha1",
			"name": "payments-factory",
			"integrations": [{"type": "slack", "slack": ` + slack + `}]
		}`
	}
	accept := []struct {
		name     string
		instance string
	}{
		{name: "both keys", instance: base(`{"allowedUserIds": ["U0123ABCDEF"], "channel": "C0123ABCDEF"}`)},
		{name: "neither key", instance: base(`{"autoRespondToThreadReplies": true}`)},
	}
	for _, tc := range accept {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateJSON(schema, []byte(tc.instance)); err != nil {
				t.Fatal(err)
			}
		})
	}
	reject := []struct {
		name     string
		instance string
		want     string
	}{
		{name: "empty user id", instance: base(`{"allowedUserIds": [""]}`), want: "shorter"},
		{name: "empty channel", instance: base(`{"channel": ""}`), want: "shorter"},
	}
	for _, tc := range reject {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateJSON(schema, []byte(tc.instance))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}
