package compaction_test

import (
	"github.com/gorenx/goren/agent"
	"github.com/gorenx/goren/compaction"
)

var _ compaction.ManualAgentContext = (agent.Agent)(nil)
