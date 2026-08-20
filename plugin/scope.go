package plugin

// ScopeKey is an opaque comparable routing identity. Its zero value is root.
type ScopeKey struct {
	token *scopeToken
}

// IsGlobal reports whether this is the Runtime root Scope.
func (selectedKey ScopeKey) IsGlobal() bool {
	return selectedKey.token == nil
}

// ScopeLineage returns child Scope keys from the farthest ancestor to the
// selected key. The root Scope is omitted.
func ScopeLineage(selectedKey ScopeKey) []ScopeKey {
	tokens := make([]*scopeToken, 0)
	for selectedToken := selectedKey.token; selectedToken != nil; selectedToken = selectedToken.parent {
		tokens = append(tokens, selectedToken)
	}
	lineage := make([]ScopeKey, len(tokens))
	for tokenIndex := range tokens {
		lineage[len(tokens)-1-tokenIndex] = ScopeKey{
			token: tokens[tokenIndex],
		}
	}
	return lineage
}

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
