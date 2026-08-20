package plugin

type scopeToken struct {
	parent *scopeToken
	depth  int
}

type scope struct {
	parent *scope
	key    ScopeKey
}

func newRootScope() *scope {
	return &scope{}
}

func newChildScope(parentScope *scope) *scope {
	scopeDepth := 1
	var parentToken *scopeToken
	if parentScope != nil {
		parentToken = parentScope.key.token
		if parentToken != nil {
			scopeDepth = parentToken.depth + 1
		}
	}
	return &scope{
		parent: parentScope,
		key: ScopeKey{
			token: &scopeToken{
				parent: parentToken,
				depth:  scopeDepth,
			},
		},
	}
}

func scopePath(sourceScope *scope) []*scope {
	lineage := make([]*scope, 0)
	for selectedScope := sourceScope; selectedScope != nil; selectedScope = selectedScope.parent {
		lineage = append(lineage, selectedScope)
	}
	return lineage
}
