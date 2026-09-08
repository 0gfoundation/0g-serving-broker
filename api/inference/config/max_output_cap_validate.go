package config

import (
	"fmt"
	"strings"
)

// Field names of the two interchangeable OpenAI output-token caps. Kept here,
// not imported from the ctrl package, because config must not depend on it.
const (
	maxTokensParam           = "max_tokens"
	maxCompletionTokensParam = "max_completion_tokens"
)

// allModelInfos returns every ModelInfo this service can serve a request with:
//
// Reads the raw ModelPricing slice, NOT HasMultiModelPricing(). The derived
// lookup map that predicate consults is built at the END of validateModelPricing,
// which runs after this — so "unifying" the two would make the whole check
// silently degrade to the service-level block on every multi-model provider.
//
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

// checkNoCapOverrides refuses a configuration in which a later body-rewriting
// pass would undo the output clamp.
//
// Both stripBodyFields and injectBodyFields run after it, and both are the
// UNION of the service-level setting and the pricing entry's own, so all four
// places have to be checked. Strip would delete the cap the clamp just set,
// leaving the flag silently inert. Inject is worse — it is server-config-wins,
// so it overwrites the clamped value and can raise the cap above the advertised
// maximum, which is the one thing the clamp promises never to happen.
func (s *Service) checkNoCapOverrides() error {
	capKeys := []string{maxTokensParam, maxCompletionTokensParam}

	checkStrip := func(where string, keys []string) error {
		for _, k := range keys {
			for _, capKey := range capKeys {
				if strings.TrimSpace(k) == capKey {
					return fmt.Errorf("invalid config: service.enforceMaxCompletionTokens cannot be combined with %s containing %q — strip runs after the clamp and would remove the cap it sets", where, capKey)
				}
			}
		}
		return nil
	}
	checkInject := func(where string, fields map[string]interface{}) error {
		for _, capKey := range capKeys {
			if _, ok := fields[capKey]; ok {
				return fmt.Errorf("invalid config: service.enforceMaxCompletionTokens cannot be combined with %s setting %q — inject runs after the clamp and would overwrite the cap, which can raise it above the advertised maximum", where, capKey)
			}
		}
		return nil
	}

	if err := checkStrip("service.stripBodyFields", s.StripBodyFields); err != nil {
		return err
	}
	if err := checkInject("service.injectBodyFields", s.InjectBodyFields); err != nil {
		return err
	}
	for i := range s.ModelPricing {
		entry := &s.ModelPricing[i]
		if err := checkStrip(fmt.Sprintf("service.modelPricing[%d].stripBodyFields", i), entry.StripBodyFields); err != nil {
			return err
		}
		if err := checkInject(fmt.Sprintf("service.modelPricing[%d].injectBodyFields", i), entry.InjectBodyFields); err != nil {
			return err
		}
	}
	return nil
}
