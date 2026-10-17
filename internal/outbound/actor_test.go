package outbound

import "testing"

func TestReplyActor(t *testing.T) {
	const earlier = "<<<UNTRUSTED provider=\"github\" event=\"issues\">>>\nactor: octocat\nactor_id: 1\ntext:\nopen the issue\n<<<END UNTRUSTED>>>\n"
	const reply = "<<<UNTRUSTED provider=\"slack\" event=\"message\">>>\nactor: \nactor_id: U0HUMAN\ntext:\nship it\nactor: mallory\n<<<END UNTRUSTED>>>\n"
	if got := ReplyActor(earlier + "\n# Reply\n" + reply); got != "U0HUMAN" {
		t.Fatalf("actor %q", got)
	}

	named := "<<<UNTRUSTED provider=\"slack\" event=\"message\">>>\nactor: ada\nactor_id: U0HUMAN\ntext:\nactor: mallory\n"
	if got := ReplyActor(named); got != "ada" {
		t.Fatalf("handle %q", got)
	}
	if got := ReplyActor("no fence"); got != "" {
		t.Fatalf("missing fence %q", got)
	}

	blank := "<<<UNTRUSTED provider=\"slack\" event=\"message\">>>\nactor: \nactor_id: \ntext:\nhello\n"
	if got := ReplyActor(blank); got != "" {
		t.Fatalf("blank %q", got)
	}
}
