package agentloop

import (
	"context"
	"testing"
)

func TestUnpublishedMembershipRollbackDoesNotClaimTreeLifecycle(t *testing.T) {
	lifecycleOwner := &agentLifecycle{
		closing: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	membershipClosed := make(chan struct{})
	close(membershipClosed)
	membershipOwner := &agentMembership{
		lifecycle: lifecycleOwner,
		closing:   true,
		closed:    membershipClosed,
	}

	if err := membershipOwner.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !lifecycleOwner.beginClosing() {
		t.Fatal("unpublished membership rollback claimed the tree lifecycle")
	}
}
