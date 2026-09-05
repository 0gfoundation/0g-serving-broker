package config

// Field names of the two interchangeable OpenAI output-token caps. Kept here,
// not imported from the ctrl package, because config must not depend on it.
const (
	maxTokensParam           = "max_tokens"
	maxCompletionTokensParam = "max_completion_tokens"
)

// allModelInfos returns every ModelInfo this service can serve a request with:
// the per-entry blocks of a multi-model service (falling back to the
// service-level one where an entry carries none), or just the service-level
// block otherwise. Nil blocks are skipped — a model with no metadata at all is
// not something the output clamp acts on.
func (s *Service) allModelInfos() []*ModelInfo {
	if len(s.ModelPricing) == 0 {
		if s.ModelInfo == nil {
			return nil
		}
		return []*ModelInfo{s.ModelInfo}
	}

	var out []*ModelInfo
	for i := range s.ModelPricing {
		mi := s.ModelPricing[i].ModelInfo
		if mi == nil {
			mi = s.ModelInfo
		}
		if mi != nil {
			out = append(out, mi)
		}
	}
	return out
}

// containsString reports whether list contains name, matched exactly.
func containsString(list []string, name string) bool {
	for _, v := range list {
		if v == name {
			return true
		}
	}
	return false
}
