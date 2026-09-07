package title

import (
	"github.com/gorenx/goren/session"
	"testing"
)

func TestAutomaticGenerationUsesHumanMessagesAndRespectsRename(t *testing.T) {
	_, events := policyFixture(t)
	for _, mode := range []AutomaticMode{AutomaticFirstPrompt, AutomaticAllPrompts} {
		allowed, err := ShouldGenerate(session.Header{}, events, events[0], mode)
		if err != nil || !allowed {
			t.Fatalf("first human prompt: %t, %v", allowed, err)
		}
		pinned := append(append([]session.Event(nil), events...), titleEntry(t, 1, "manual"))
		allowed, err = ShouldGenerate(session.Header{}, pinned, events[0], mode)
		if err != nil || allowed {
			t.Fatalf("manual title: %t, %v", allowed, err)
		}
		newer := events[0].Clone()
		newer.Seq = 2
		allowed, err = ShouldGenerate(session.Header{}, append(append([]session.Event(nil), events...), newer), events[0], mode)
		if err != nil || allowed {
			t.Fatalf("stale human event: %t, %v", allowed, err)
		}
	}
	parent := session.SessionID("parent")
	allowed, err := ShouldGenerate(session.Header{ParentSession: &parent}, events, events[0], AutomaticFirstPrompt)
	if err != nil || allowed {
		t.Fatalf("child first prompt: %t, %v", allowed, err)
	}
}
