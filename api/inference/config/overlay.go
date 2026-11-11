package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v2"
)

// The config overlay: the part of a provider's configuration that may change while the
// CVM runs, without a redeploy.
//
// # Why it exists
//
// A private (TeeML) deployment fixes everything that decides where a prompt goes and who
// can read it in the deployment itself: the compose file, which the quote's compose hash
// covers. Changing any of that is a redeploy, and a verifier reads it from the manifest it
// already authenticates. That leaves the settings that change often and carry no privacy
// weight — prices, rate limits, the free-use whitelist, settlement timing — which an
// operator still needs to change without reloading a model. The overlay is that set, and
// only that set: a second file, merged over the main one at load, whose every key must be
// on overlayAllow.
//
// # Why an allowlist and not a denylist
//
// A key added to the broker later is frozen until someone decides otherwise. A denylist
// would make every new key mutable by default, including the next upstream URL.
//
// # What is deliberately not here
//
//   - servingUrl: the address the broker publishes on-chain. On 2026-11-05 an attacker
//     holding provider keys rewrote on-chain service URLs to probe the router's network;
//     a URL is not a price.
//   - priceFeed: it names the price sources (URLs) and carries an API key.
//   - revenueTransfer: it names where the provider's earnings are sent.
//   - every upstream, logging, request-rewriting and model-identity field (targetUrl,
//     upstreamModel, modelAliases, canonicalId, inject/stripBodyFields, requestProfile,
//     providerType, logger, database, assay, lora, zk, monitor, overloadGuard.metricsUrl).
//
// modelPricing is a list; an overlay entry names an existing model by `model` and may
// change only its price fields. It cannot add a model, because a new model may bring an
// upstream of its own.

// OverlayFileEnvVar names the overlay file. Unset, there is no overlay and the main config
// is used as is.
const OverlayFileEnvVar = "CONFIG_OVERLAY_FILE"

// overlayRule is one node of the allowlist. whole admits the key and everything under it;
// children admits only the named keys; listKey turns the node into a list of mappings
// matched to the base list by that key, each judged by item.
type overlayRule struct {
	whole    bool
	children map[string]*overlayRule
	listKey  string
	item     *overlayRule
}

var overlayLeaf = &overlayRule{whole: true}

func overlayKeys(keys ...string) *overlayRule {
	r := &overlayRule{children: map[string]*overlayRule{}}
	for _, k := range keys {
		r.children[k] = overlayLeaf
	}
	return r
}

// priceKeys are the price fields that exist both on the service and on a modelPricing
// entry.
var priceKeys = []string{
	"inputPrice", "outputPrice",
	"inputPriceUSDPerMillionTokens", "outputPriceUSDPerMillionTokens",
	"outputPriceUSDPerSecond",
}

var overlayAllow = func() *overlayRule {
	service := overlayKeys(append([]string{
		"priceDenomination", "outputPriceUSDPerImage",
		"enforceMaxCompletionTokens",
		"modelInfo", "ownedBy", "providerName", "providerCountry",
	}, priceKeys...)...)

	entry := overlayKeys(append([]string{
		"tiers", "billing", "cacheTokenBilling", "modelInfo",
	}, priceKeys...)...)
	service.children["modelPricing"] = &overlayRule{listKey: "model", item: entry}

	root := overlayKeys(
		"whitelist", "concurrencyLimit",
		"tieredPricing", "cacheTokenBilling", "modalityPricing",
		"providerHttp", "chatCacheExpiration",
		"interval", "settlement", "gasPrice", "maxGasPrice",
		"reconciliation", "userUsageStats",
	)
	root.children["service"] = service
	root.children["overloadGuard"] = overlayKeys("enabled", "maxQueueRequests", "maxTokenUsage", "pollInterval", "retryAfter")
	return root
}()

// ApplyOverlay merges overlay over base and returns the merged config and the paths the
// overlay set. It refuses an overlay that is not a mapping, names a key the allowlist does
// not admit, or names a modelPricing model the base does not have.
//
// Maps merge key by key; anything else the overlay names replaces the base value. An
// empty overlay returns base unchanged.
func ApplyOverlay(base, overlay []byte) ([]byte, []string, error) {
	if len(bytes.TrimSpace(overlay)) == 0 {
		return base, nil, nil
	}
	var ov map[interface{}]interface{}
	if err := yaml.Unmarshal(overlay, &ov); err != nil {
		return nil, nil, fmt.Errorf("config overlay is not a YAML mapping: %w", err)
	}
	if len(ov) == 0 {
		return base, nil, nil
	}
	var b map[interface{}]interface{}
	if err := yaml.Unmarshal(base, &b); err != nil {
		return nil, nil, fmt.Errorf("base config is not a YAML mapping: %w", err)
	}
	if b == nil {
		b = map[interface{}]interface{}{}
	}

	var paths, refused []string
	merged := mergeOverlayMap(b, ov, overlayAllow, "", &paths, &refused)
	if len(refused) > 0 {
		sort.Strings(refused)
		return nil, nil, fmt.Errorf("config overlay may only change prices, limits, the whitelist and settlement timing; refused: %s", strings.Join(refused, "; "))
	}
	out, err := yaml.Marshal(merged)
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(paths)
	return out, paths, nil
}

func mergeOverlayMap(base, ov map[interface{}]interface{}, rule *overlayRule, prefix string, paths, refused *[]string) map[interface{}]interface{} {
	out := make(map[interface{}]interface{}, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range ov {
		key, ok := k.(string)
		path := prefix + fmt.Sprint(k)
		if !ok {
			*refused = append(*refused, path+" (not a string key)")
			continue
		}
		var child *overlayRule
		if rule.whole {
			child = overlayLeaf
		} else {
			child = rule.children[key]
		}
		if child == nil {
			*refused = append(*refused, path+" (not changeable at runtime)")
			continue
		}
		switch {
		case child.listKey != "":
			out[key] = mergeOverlayList(base[key], v, child, path, paths, refused)
		case child.whole:
			if bm, ok := base[key].(map[interface{}]interface{}); ok {
				if om, ok := v.(map[interface{}]interface{}); ok {
					out[key] = mergeOverlayMap(bm, om, overlayLeaf, path+".", paths, refused)
					continue
				}
			}
			out[key] = v
			*paths = append(*paths, path)
		default:
			om, ok := v.(map[interface{}]interface{})
			if !ok {
				*refused = append(*refused, path+" (must be a mapping)")
				continue
			}
			bm, _ := base[key].(map[interface{}]interface{})
			if bm == nil {
				bm = map[interface{}]interface{}{}
			}
			out[key] = mergeOverlayMap(bm, om, child, path+".", paths, refused)
		}
	}
	return out
}

// mergeOverlayList merges a list of mappings matched by rule.listKey. Every overlay entry
// must name an entry the base already has.
func mergeOverlayList(base, ov interface{}, rule *overlayRule, path string, paths, refused *[]string) interface{} {
	ol, ok := ov.([]interface{})
	if !ok {
		*refused = append(*refused, path+" (must be a list)")
		return base
	}
	bl, _ := base.([]interface{})
	out := make([]interface{}, len(bl))
	copy(out, bl)
	index := map[string]int{}
	for i, e := range bl {
		if m, ok := e.(map[interface{}]interface{}); ok {
			index[fmt.Sprint(m[rule.listKey])] = i
		}
	}
	for _, e := range ol {
		om, ok := e.(map[interface{}]interface{})
		if !ok {
			*refused = append(*refused, path+" (entries must be mappings)")
			continue
		}
		name := fmt.Sprint(om[rule.listKey])
		i, ok := index[name]
		if !ok || om[rule.listKey] == nil {
			*refused = append(*refused, fmt.Sprintf("%s[%s=%s] (no such entry in the base config; adding one is a redeploy)", path, rule.listKey, name))
			continue
		}
		rest := make(map[interface{}]interface{}, len(om))
		for k, v := range om {
			if k != rule.listKey {
				rest[k] = v
			}
		}
		bm, _ := out[i].(map[interface{}]interface{})
		out[i] = mergeOverlayMap(bm, rest, rule.item, fmt.Sprintf("%s[%s=%s].", path, rule.listKey, name), paths, refused)
	}
	return out
}

// readOverlay returns the overlay file's content, or nil when there is no overlay. A
// missing file is no overlay; any other read error is returned.
func readOverlay() ([]byte, error) {
	path := os.Getenv(OverlayFileEnvVar)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config overlay %s: %w", path, err)
	}
	return data, nil
}

// OverlayDigest is the sha256 of the overlay file's content, or "" when there is none.
// Processes compare it over time to notice a new overlay.
func OverlayDigest() (string, error) {
	data, err := readOverlay()
	if err != nil || data == nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
