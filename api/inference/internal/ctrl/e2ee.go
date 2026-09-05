package ctrl

// This file implements the provider (enclave) side of the 0g-pc end-to-end
// encryption protocol (SPEC.md §5–§7): unsealing the sensitive request fields a
// client sealed to this enclave's HPKE key, and sealing the sensitive response
// fields back to the client's ephemeral key. The wire format and crypto are
// provided by github.com/0gfoundation/0g-pc-e2ee/protocol (imported byte-for-byte);
// this layer wires them into the broker's proxy/billing/signing path and adds
// the broker-specific policy checks the protocol package deliberately leaves to
// the caller.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"slices"
	"strings"

	pccrypto "github.com/0gfoundation/0g-pc-e2ee/protocol/crypto"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/proof"
	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
)

// ErrE2EEKeyMismatch marks a sealed request whose key_id is not the enclave's
// current enc key — e.g. after a provider upgrade rotated the measurement-tied
// key while the router/client still hold the old one. It is a RETRIABLE,
// self-healing condition (the client should re-fetch + re-verify the enc key and
// re-seal), distinct from a hard fail-closed unseal failure (tampered AAD, bad
// envelope, unusable ephemeral key). The proxy maps it to HTTP 409 with a
// machine-recognizable "e2ee_key_mismatch" message prefix so the router
// (0g-router#618) re-syncs this provider rather than bouncing a 4xx to the user;
// all other unseal failures stay 400.
var ErrE2EEKeyMismatch = errors.New("e2ee_key_mismatch")

const (
	// e2eeBodyMarker is the reserved top-level field carrying the sealing metadata
	// (SPEC §5). A request is treated as sealed iff it has this as a top-level JSON
	// key — matching the router, which routes on the body field, not a header.
	// (A header signal may be added later; the field is the source of truth today.)
	e2eeBodyMarker = "_e2ee"

	// clientEphPubLen is the byte length of the client's response ephemeral X25519
	// public key (SPEC §3 suite).
	clientEphPubLen = 32

	// anthropicFrameType is the cleartext field an Anthropic response frame names
	// its own shape in, and anthropicMessageStop the event a completed turn ends
	// with (SPEC §7.2). The wire package owns the full taxonomy — including which
	// shapes are terminal, which is asked of it rather than restated here; the
	// broker needs these two only to SYNTHESIZE the event a truncated stream never
	// got.
	anthropicFrameType   = "type"
	anthropicMessageStop = "message_stop"

	// failureField is the field an upstream reports a mid-stream failure in, on
	// both surfaces this broker serves — Anthropic's `error` shape carries it as
	// its content field, and an OpenAI-compatible stream sends a bare
	// `{"error": …}` chunk. Named here because "does this frame report a failure"
	// cannot be answered from a profile's sealed set alone: chat's is only
	// ["choices"], so its error chunk is content the taxonomy does not name.
	//
	// Two places act on it, for the two things that can go wrong with a report:
	// prepareFrameForSealing SEALS it where a single-shape profile would have left
	// it cleartext (an error message can quote the request), and
	// handleFrameAfterFinal FAILS the stream on one arriving behind the final
	// frame (dropping it would tell the client its turn succeeded). The first is
	// in the step BOTH seal paths share, which is what keeps the rule from
	// applying to streams only.
	failureField = "error"

	// doneSentinel is the OpenAI-style stream terminator, as it appears in a
	// `data:` line's payload — matched after the payload is parsed out, because
	// SSE makes the space after the colon optional and `data:[DONE]` is the same
	// sentinel as `data: [DONE]`.
	doneSentinel = "[DONE]"

	// CtxKeyE2EESealed marks (bool) that the current request arrived sealed, so the
	// response path knows to seal its reply.
	CtxKeyE2EESealed = "e2eeSealed"
	// CtxKeyE2EEClientEphPub holds the client's response ephemeral X25519 public
	// key (pccrypto.PublicKey) extracted from the request envelope (SPEC §7).
	CtxKeyE2EEClientEphPub = "e2eeClientEphPub"
	// CtxKeyE2EEPlaintextReq holds the reconstructed plaintext request bytes
	// ([]byte) captured immediately after unsealing, BEFORE the proxy's upstream
	// rewrites (model enforcement, stream_options injection, …). Retained for
	// observability/audit; the §8 signature no longer binds plaintext (see
	// CtxKeyE2EEReqBindHash).
	CtxKeyE2EEPlaintextReq = "e2eePlaintextReq"
	// CtxKeyE2EEReqBindHash holds the §8 request binding hash ([32]byte =
	// proof.FrameBindingHash of the sealed request: sha256(sha256(aad)‖sha256(ct)))
	// captured at unseal time. The response signature binds the on-wire
	// ciphertext, but the proxy replaces the sealed request with its plaintext
	// before forwarding, so the response path can no longer recompute it — the
	// binding is stashed here and combined with the response hash at sign time.
	CtxKeyE2EEReqBindHash = "e2eeReqBindHash"
	// CtxKeyE2EEProfile holds the wire.Profile (SPEC §5.1) the request was opened
	// under, resolved once at unseal time from the service type AND the API
	// surface the request arrived on.
	//
	// The response path reads it back rather than re-deriving it, so a response
	// cannot be sealed under a profile whose rules were never applied to its
	// request. Re-deriving looked equivalent while every service type had exactly
	// one surface; it stopped being equivalent with /v1/messages, which is the
	// SAME chatbot service type as /v1/chat/completions — so the chat literal at
	// the non-streaming call site was simply wrong for it, and being wrong here is
	// silent (identical wire format, plausible frames, content in the clear).
	CtxKeyE2EEProfile = "e2eeProfile"
)

// e2eeResponseUnboundFields are declared in every sealed response's
// `unbound_fields` (SPEC §5.2). They are cleartext fields EXCLUDED from the seal
// AAD, so the router may inject or rewrite them on the way back to the client
// (broker → router → client) without breaking the client's Open — they are not
// covered by the §8 signature. Per the §8 corollary a router-injected value is
// not cryptographically trusted (trust comes from on-chain settlement), so these
// MUST stay unbound rather than being bound/signed fields:
//   - "model": the router substitutes the served model back to the alias the
//     client requested, so it must be rewritable without invalidating the seal.
//   - "x_0g_trace": observability metadata the router injects downstream.
var e2eeResponseUnboundFields = []string{"model", "x_0g_trace"}

// maxHeadersExamined bounds how many multipart parts AND Content-Disposition
// headers multipartCarriesE2EEPart will read before it stops and refuses. One
// shared budget, because both loops are separately unbounded: the outer over
// parts, the inner over the dispositions a single part declares.
//
// Three orders of magnitude above any real transcription or image-edit form,
// and ~1000x below the 3.7 million minimum-size parts that fit in 32 MiB.
//
// About that "32 MiB": it is RequestSizeLimitMiddleware, and it is registered on
// the sync proxy's serviceGroup ONLY (proxy.go). The async submit routes hang
// off r.Group("/v1") with cors and the rate limiter and no size limit, so on
// /v1/async/images/edits neither the part count nor the body length is bounded
// by anything but what the client sends. This budget is what bounds the
// AMPLIFICATION on both paths — the cost of this check against the cost of
// reading the body at all — and on the sync path the size limit also bounds the
// absolute cost. The unbounded read on the async route predates this check and
// is not widened by it; adding a size limit there would reject bodies that work
// today, which is a decision for its own change rather than a comment.
const maxHeadersExamined = 4096

// hasE2EEMarker is a cheap substring pre-check to skip the JSON parse on the vast
// majority of (non-sealed) requests. A match is not proof of a sealed request —
// the substring could appear inside message content — so MaybeUnsealRequest
// confirms a genuine top-level "_e2ee" key before committing to fail-closed.
func hasE2EEMarker(reqBody []byte) bool {
	return bytes.Contains(reqBody, []byte(e2eeBodyMarker))
}

// couldNameE2EEPart reports whether a body SPELLS something that a part named
// "_e2ee" would have to spell — the literal marker, or an RFC 2231 `name*`
// attribute in any case.
//
// It does NOT report whether the body might be hiding such a part, and the
// difference is the whole reason it gates nothing but the fail-closed branches.
// It cannot see an encoded word, or a quoted pair, or any other spelling that
// resolves to the marker without containing it; three separate rounds of review
// found one of those. A summary of "might be hiding it" would be a claim this
// function is structurally unable to make.
//
// It gates the FAIL-CLOSED branches only, never detection. This is a heuristic
// over SPELLINGS, and spellings do not enumerate: it cannot see a name the
// parser resolves from an encoding, so anything that gates detection on it leaks
// whatever it failed to anticipate. Detection asks the parser instead
// (multipartCarriesE2EEPart), which is exact for every body Go can parse; this
// covers the bodies it cannot, where the failure direction is forwarding a
// MALFORMED request rather than an envelope.
//
// Case is folded for "name*" because the parser folds parameter names; the
// literal marker is matched exactly because parameter VALUES are not folded, so
// a part named `_E2EE` is a different field no parser resolves to the marker.
func couldNameE2EEPart(reqBody []byte) bool {
	return hasE2EEMarker(reqBody) || mentionsEncodedName(reqBody)
}

// mentionsEncodedName reports whether the body contains the RFC 2231 parameter
// prefix "name*" AT A PARAMETER POSITION, in any case. One pass, one byte at a
// time, no allocation — the body may be tens of megabytes of audio.
//
// The parameter position is what makes this selective. `filename*` also contains
// "name*" and is what browsers emit for a non-ASCII upload name (RFC 8187), so
// matching it anywhere makes the gate true for essentially every
// internationalised form — and the gate decides the fail-closed branches:
//
//	filename*=UTF-8''caf%C3%A9.png  -> gate=true,  REFUSED
//	filename="cafe.png"             -> gate=false, forwarded
//
// Selectivity therefore has to be measured against real form bodies, not random
// ones: `name*` does not occur in 32 MiB of random bytes, but `filename*` occurs
// in most internationalised uploads. declaresEncodedName excludes it on a token
// boundary for the same reason.
//
// Cost is FLAT by design, ~51 ms per 32 MiB whatever the body contains. An
// anchored bytes.IndexByte scan is 9x faster on typical input and 8x slower on a
// '*'-dense one — the wrong trade for a check that runs before inference on
// requests it exists to reject, since a cliff an attacker picks the body for is
// worse than a higher floor. It is also not where this check spends its time:
// enumerating the parts costs ~750 ms for 32 MiB of tiny ones against ~22 ms for
// one large part, so optimise multipartCarriesE2EEPart instead.
//
// The fold is letters-only rather than the usual `b|0x20`, which is not
// stylistic: 0x20 maps '\n' (0x0A) onto '*' (0x2A), reporting "name\n" as a
// mention. TestMentionsEncodedName sweeps all 256 bytes through each of the five
// positions to hold that. Restarting needs no backtracking beyond index 1,
// because 'n' occurs only at index 0 of the needle.
func mentionsEncodedName(reqBody []byte) bool {
	const needle = "name*"
	matched := 0
	atBoundary := false
	for i := 0; i < len(reqBody); i++ {
		b := reqBody[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if b == needle[matched] {
			if matched == 0 {
				atBoundary = i == 0 || isParamBoundary(reqBody[i-1])
			}
			matched++
			if matched == len(needle) {
				if atBoundary {
					return true
				}
				matched = 0 // the tail of a longer attribute, e.g. filename*
			}
			continue
		}
		if b == needle[0] {
			matched = 1
			atBoundary = i == 0 || isParamBoundary(reqBody[i-1])
		} else {
			matched = 0
		}
	}
	return false
}

// isParamBoundary reports whether b can precede the start of a header
// parameter: a semicolon, linear whitespace, or a header fold.
func isParamBoundary(b byte) bool {
	switch b {
	case ';', ' ', '\t', '\r', '\n':
		return true
	}
	return false
}

// declaresEncodedName reports whether a Content-Disposition RFC 2231-encodes its
// `name` parameter, in any of the forms that notation has: `name*=`, and the
// `name*0=` / `name*0*=` continuation segments, case-insensitively because
// parameter names are.
//
// The match must sit at a PARAMETER POSITION, and neither neighbour of the match
// establishes that. `filename*=` and `filename="name*.wav"` both contain "name*"
// and both are ordinary, and so does `filename="name*=x.wav"`, which satisfies
// any check on what FOLLOWS as well. So this walks the disposition tracking
// quoted strings and tests only after a `;` outside quotes. Desyncing requires a
// disposition that does not parse, which the caller's gated fail-closed branch
// covers.
//
// This is a tokeniser, not a decoder, and that is why writing one here is not
// the mistake refused in multipartCarriesE2EEPart: what an encoded name MEANS is
// the parser's job, but where a parameter STARTS is unambiguous grammar with no
// charset in it.
func declaresEncodedName(disposition string) bool {
	inQuotes := false
	atParamStart := false
	for i := 0; i < len(disposition); i++ {
		switch c := disposition[i]; {
		case inQuotes:
			switch c {
			case '\\':
				i++ // a quoted pair: whatever follows is not a closing quote
			case '"':
				inQuotes = false
			}
		case c == '"':
			inQuotes = true
		case c == ';':
			atParamStart = true
		case c == ' ' || c == '\t':
			// Leading whitespace does not end the parameter position.
		default:
			if atParamStart {
				if isEncodedNameAttr(disposition[i:]) {
					return true
				}
				atParamStart = false
			}
		}
	}
	return false
}

// isEncodedNameAttr reports whether s BEGINS with an RFC 2231-encoded `name`
// attribute followed by its `=`. The three shapes are `name*=`, `name*<n>=` and
// `name*<n>*=`; anything else that merely starts with "name*" — `name*foo=`, or
// a bare `name*` at the end of the header — is not one, and neither is
// `namespace=`.
func isEncodedNameAttr(s string) bool {
	const attr = "name*"
	if len(s) < len(attr) || !strings.EqualFold(s[:len(attr)], attr) {
		return false
	}
	rest := s[len(attr):]
	for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
		rest = rest[1:] // the continuation index of name*<n>
	}
	if strings.HasPrefix(rest, "*") {
		rest = rest[1:] // the encoding marker of name*<n>*
	}
	return strings.HasPrefix(rest, "=")
}

// isEncodedWord reports whether a parameter value is an RFC 2047 encoded word —
// `=?charset?encoding?text?=`. Only the delimiters are checked, deliberately:
// what the word decodes to is the question this guard refuses to answer for
// itself, here as in declaresEncodedName.
//
// Surrounding whitespace is trimmed first, because RFC 2047 §2 permits linear
// white space around an encoded word and a prefix/suffix test does not:
//
//	name="=?utf-8?B?X2UyZWU=?="    -> refused
//	name=" =?utf-8?B?X2UyZWU=?="   -> FORWARDED, before the trim
//	name="=?utf-8?B?X2UyZWU=?= "   -> FORWARDED, before the trim
//
// Weaker than the charset gap: exploiting it needs an upstream that both decodes
// encoded words in parameter values AND trims the result (javax.mail keeps the
// leading space, yielding " _e2ee"). But the branch's contract is to refuse
// without deciding what the word meant, and the spellings it covers should not
// turn on a space.
func isEncodedWord(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) > 4 && strings.HasPrefix(value, "=?") && strings.HasSuffix(value, "?=")
}

// mentionsEncodedWord reports whether ONE header spells the RFC 2047 encoded-word
// introducer anywhere in it. It extends couldNameE2EEPart for the two branches
// that fire when a part header does not parse, and only for those.
//
// It exists because the argument that kept those branches gated does not hold.
// The claim was that making the marker-bearing header unparseable leaves its name
// a guess, so the heuristic gives up nothing — but the marker can be spelled in an
// unparseable header as an encoded word, which is precisely the spelling
// couldNameE2EEPart is structurally unable to see. Every way this file already
// enumerates for breaking a Content-Type breaks a PART header just as well:
//
//	name="=?utf-8?B?X2UyZWU=?="            parses -> refused by isEncodedWord
//	name=x; name="=?utf-8?B?X2UyZWU=?="    duplicate parameter -> was forwarded
//	name="=?utf-8?B?X2UyZWU=?=             unclosed quote      -> was forwarded
//	name="=?utf-8?B?X2UyZWU=?="; =junk     junk parameter      -> was forwarded
//
// A lenient parser reads a name out of all three — javax.mail takes the last of a
// duplicate pair and decodes encoded words in parameter values — so the refusal
// must not turn on whether Go can parse the header around the marker.
//
// The scope is ONE HEADER, and that is the whole reason this is affordable.
// Widening couldNameE2EEPart itself to "=?" was rejected for good cause: the
// sequence occurs ~516 times by chance in 32 MiB of random body, which makes the
// gate always-true. A part header is a few hundred bytes of structured grammar,
// where "=?" is the RFC 2047 signal and essentially nothing else; the malformed
// dispositions real clients send (`filename="my"file.txt"`) do not contain it.
//
// Deliberately looser than isEncodedWord, which decides an EXACT refusal on a
// parsed value. Here nothing parsed, so there is no value to anchor to and no
// position to demand — the question is only whether this broken header could be
// spelling the marker in the way the other gate misses.
func mentionsEncodedWord(header string) bool {
	return strings.Contains(header, "=?")
}

// unquotePairs removes RFC 2045 quoted-pair backslashes the way a conformant
// parser does: inside a quoted string every `\` escapes the character after it.
//
// Go does not. mime.ParseMediaType unescapes a backslash only where it precedes
// a tspecial, so `name="\_e2ee"` comes back as `\_e2ee` — while javax.mail and
// Ruby's mail read `_e2ee`. Same one-encoding-over shape as the RFC 2231 charset
// gap, and it gets the same treatment in resolvesToMarker.
//
// Dropping every backslash is not a faithful decode of the ORIGINAL header — Go
// has already consumed some of them — so a name that legitimately contains a
// backslash is compared as though it did not. That errs toward refusing, on a
// spelling no form client produces.
func unquotePairs(name string) string {
	return strings.ReplaceAll(name, `\`, "")
}

// resolvesToMarker reports whether a parsed parameter value is the reserved
// marker under any reading a conformant parser might give it: as written, with
// linear white space trimmed (RFC 2047 §2, and RFC 2045 folding around a
// parameter value), with quoted pairs resolved, or both.
//
// Both, in that order, is the case a single pass misses: a quoted pair can
// protect the very space that has to be trimmed, so `\ _e2ee` needs the pair
// resolved BEFORE the trim, where ` \_e2ee` does not care. Trimming after
// unquoting subsumes the other order — if the value is the marker once outer
// white space and backslashes are gone, it is also the marker with the
// backslashes removed first — so these four readings are the whole closure
// rather than a sample of it, and a test asserts that against a fixpoint.
//
// Whitespace and backslashes in a form-field name are not something any client
// emits, so refusing them costs nothing real.
func resolvesToMarker(name string) bool {
	for _, s := range []string{name, unquotePairs(name)} {
		if s == e2eeBodyMarker || strings.TrimSpace(s) == e2eeBodyMarker {
			return true
		}
	}
	return false
}

// looksMultipart reports whether a Content-Type CLAIMS to be multipart, by a
// case-insensitive prefix test on the raw string — deliberately without parsing
// it.
//
// Parsing here fails OPEN. mime.ParseMediaType rejects a duplicate parameter, a
// trailing junk parameter and an unclosed quote, so deciding on the parse lets
// all three skip the guard entirely — envelope intact — and leaves the
// fail-closed branch written for exactly that case unreachable. Measured:
//
//	multipart/form-data; boundary=x; boundary=y  -> duplicate parameter
//	multipart/form-data; boundary=x; =junk       -> invalid media parameter
//	multipart/form-data; boundary="x             -> invalid media parameter
//
// A lenient upstream reads parts out of all three. So whether this is OUR
// business is decided on the raw header, and the parse — which can fail — only
// decides which branch inside handles it.
//
// Any subtype, not just form-data, because the question is "can this body carry
// parts" and every multipart/* can. Matching only form-data left the hole
// reachable by changing one word of the Content-Type.
func looksMultipart(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimLeft(contentType, " \t")), "multipart/")
}

// nameVerdict applies the readings that decide a declared part name to ONE
// header, so that both headers which can carry a name get exactly the same
// treatment. Written once for that reason: finding 28 was the Content-Type
// missing checks the Content-Disposition had, and two copies is how that
// happens.
//
// `where` names the header for the message; `header` is its raw value, needed by
// declaresEncodedName because the parser drops what it cannot decode; `name` is
// the value the parser resolved.
func nameVerdict(where, header, name string) (bool, string) {
	switch {
	case name == e2eeBodyMarker:
		return true, fmt.Sprintf("a multipart part is named %q", e2eeBodyMarker)
	case resolvesToMarker(name):
		return true, fmt.Sprintf("%s names a part %q as a conformant parser reads it, trimming linear white space and resolving RFC 2045 quoted pairs where mime.ParseMediaType does neither, so the name it declares cannot be ruled out", where, e2eeBodyMarker)
	case isEncodedWord(name):
		return true, fmt.Sprintf("%s names a part with an RFC 2047 encoded word, which mime.ParseMediaType returns undecoded but other parsers resolve, so the name it declares cannot be ruled out", where)
	case declaresEncodedName(header):
		return true, fmt.Sprintf("%s RFC 2231-encodes a part's name, which mime.ParseMediaType resolves only for us-ascii and utf-8 (dropping the rest silently), so the name it declares cannot be ruled out", where)
	}
	return false, ""
}

// recoverBoundary extracts the boundary from a Content-Type that
// mime.ParseMediaType rejected: everything from `boundary=` to the next `;`,
// trimmed of surrounding white space and one layer of quotes.
//
// It exists because RFC 2046 §5.1.1 admits boundary characters RFC 2045 calls
// tspecials — `: = ? / ' ( ) + , - . _` and, in bchars, space — so an UNQUOTED
// but entirely legal boundary makes ParseMediaType fail. Measured:
//
//	boundary=----WebKitFormBoundaryABC123  parse ok
//	boundary=abc:def                       parse err, recovered `abc:def`
//	boundary=a?b/c                         parse err, recovered `a?b/c`
//	boundary=a+b,c-d.e_f                   parse err, recovered `a+b,c-d.e_f`
//	boundary=boundary with space           parse err, recovered `boundary with space`
//
// Without recovery each of those hit the structural refusal — a 400 on a legal
// request, pre-authentication on the sync path. With it they are enumerated and
// judged on their part names like any other body, which is both fewer false
// positives AND real detection where there was only a blanket verdict.
//
// The parameter name is matched at a parameter position and case-insensitively,
// so `xboundary=` and a `boundary=` inside another parameter's value do not
// count. Recovery is deliberately permissive about the VALUE — a body's real
// delimiter is whatever it is, and enumerating with a wrong guess finds no
// parts, which lands on the same structural refusal as recovering nothing.
func recoverBoundary(contentType string) string {
	const attr = "boundary="
	for i := 0; i+len(attr) <= len(contentType); i++ {
		if !strings.EqualFold(contentType[i:i+len(attr)], attr) {
			continue
		}
		if i > 0 && !isParamBoundary(contentType[i-1]) {
			continue
		}
		value := contentType[i+len(attr):]
		if end := strings.IndexByte(value, ';'); end >= 0 {
			value = value[:end]
		}
		value = strings.TrimSpace(value)
		if len(value) > 1 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}
		return value
	}
	return ""
}

// countLineStartsWith counts the lines of the body that begin with prefix. Both
// callers use it to ask a structural question about a body that failed to parse:
// does it still declare part-shaped content that this enumeration never reached?
//
// A line start is taken as "at the body start, or after LF" rather than after
// CRLF, deliberately: a body using bare LF line endings is malformed but lenient
// parsers read it, and anchoring on CRLF would UNDERCOUNT such a body and
// forward it. Overcounting only happens when part content contains the prefix at
// a line start, and where it does, a parser splits there too.
//
// bytes.Index rather than the byte-at-a-time scan mentionsEncodedName needs: the
// needle is a fixed multi-byte string, so the platform's optimised search
// applies. It runs at most once per request, on an error path that returns
// immediately.
func countLineStartsWith(reqBody []byte, prefix string) int {
	needle := []byte(prefix)
	n := 0
	for i := 0; i+len(needle) <= len(reqBody); {
		j := bytes.Index(reqBody[i:], needle)
		if j < 0 {
			break
		}
		at := i + j
		if at == 0 || reqBody[at-1] == '\n' {
			n++
		}
		i = at + len(needle)
	}
	return n
}

// declaresUnreadableParts reports whether a body that could not be enumerated
// with the declared boundary nonetheless contains something part-shaped: a line
// opening with `--`, which is where a part begins under SOME boundary. A parser
// that cannot use the declared boundary may resolve a different one — several
// take it from the first such line — so those bytes are part headers this
// enumeration never read.
//
// The distinction this draws is the reason it exists rather than an
// unconditional refusal: a JSON body sent with a multipart Content-Type has no
// such line, so no parser reads a part from it under any boundary, and it is
// forwarded on a structural fact rather than on a spelling heuristic.
func declaresUnreadableParts(reqBody []byte) bool {
	return countLineStartsWith(reqBody, "--") > 0
}

// multipartCarriesE2EEPart reports whether a multipart body declares a part named
// "_e2ee" — a sealed envelope smuggled into the one request shape the JSON checks
// cannot see (SPEC §5.3.1). The string is the reason, for the rejection message.
//
// It exists because every other sealed-request check parses the body as JSON, and
// a multipart body is not JSON: "detect _e2ee, else pass through" reads the parse
// failure as NOT SEALED and forwards the envelope in the clear. §5.3.1 names the
// rule that breaks — a body that cannot be parsed as an envelope is not thereby
// an unsealed body.
//
// The check is on PART NAMES, never the raw bytes, and that cuts both ways: a
// substring rule would refuse a transcription whose `prompt` legitimately
// mentions the marker, and would miss the name whenever it is encoded.
//
// "Declares a name" means both headers that can carry one (Content-Disposition,
// and Content-Type per RFC 2045 §5), read directly rather than via
// Part.FormName() — which answers "" for any disposition that is not exactly
// `form-data`, while lenient parsers read `name` regardless of the type — and
// read across every value the part declares, since Header.Get answers with the
// first of several.
//
// Comparison is CASE-SENSITIVE, because form field names are: `_E2EE` is a
// different field. It is not literal, though — resolvesToMarker compares every
// reading a conformant parser might give the value.
//
// Two kinds of branch below, and the distinction carries the design. EXACT ones
// (a resolved name, a header that demonstrably encodes one, names that were
// never read at all) refuse outright. HEURISTIC ones — where a header cannot be
// parsed, so nothing can be asked — refuse only when couldNameE2EEPart also
// holds, making the set "malformed AND could be naming the marker" rather than
// "malformed". That gated set is deliberately not closed; see the branches.
//
// Parts whose header never completes are not covered and need no cover: Go drops
// one with a plain io.EOF, and a header that never terminates has no body after
// it to hold an envelope. Its neighbour — a header that terminates with a
// malformed name — makes ParseMediaType error and is refused.
func multipartCarriesE2EEPart(contentType string, reqBody []byte) (bool, string) {
	if !looksMultipart(contentType) {
		return false, ""
	}
	_, params, err := mime.ParseMediaType(contentType)
	boundary := params["boundary"]
	if err != nil {
		// A parse failure is not the end of the question: the boundary may be
		// perfectly legal and merely unquoted (see recoverBoundary). Recovering it
		// turns a blanket refusal into an actual enumeration.
		boundary = recoverBoundary(contentType)
	}
	if boundary == "" {
		// UNGATED, like the budget and nested branches and unlike the per-header
		// ones. With no readable boundary NOTHING is enumerated, so this is the
		// strongest form of "the names were never read" — and the gating argument
		// that holds one branch down does not reach here: what is broken is the
		// REQUEST's Content-Type, while the marker-bearing part header inside the
		// body is intact and readable by any parser that resolves the boundary
		// differently. The name is not a guess; it is simply never looked at.
		//
		// A gate here is evadable by the one spelling it cannot see: a duplicate
		// boundary parameter in front of an otherwise perfect part named
		// `=?utf-8?B?X2UyZWU=?=`, which is neither the literal marker nor `name*`.
		//
		// STRUCTURAL, not unconditional: refuse when the body still contains a
		// part-shaped line, forward when it contains none. A JSON body sent with a
		// multipart Content-Type is the second case and stays forwarded.
		//
		// Two causes, two wordings: a Content-Type that does not parse, and one
		// that parses with no boundary at all. Formatting err in both renders the
		// second as "could not be read (<nil>)".
		if declaresUnreadableParts(reqBody) {
			why := "its multipart boundary is missing from the Content-Type"
			if err != nil {
				why = fmt.Sprintf("its multipart boundary could not be read or recovered (%v)", err)
			}
			return true, "the body declares parts that cannot be enumerated because " + why + ", so a smuggled envelope cannot be ruled out"
		}
		return false, ""
	}

	// The gate scans the whole body and depends on nothing but reqBody, so it is
	// computed at most once — and lazily, so a well-formed body never pays for it
	// at all. The disposition branch `continue`s rather than returning, so a
	// per-part call costs parts x len(body) for an identical answer: 97 ms / 4.1 s
	// / 16.4 s at 1 / 50 / 200 malformed parts in 32 MiB, pre-authentication (the
	// sync proxy reaches here ~90 lines before ValidateSession). See
	// maxHeadersExamined for what bounds the rest.
	examined := 0
	partsRead := 0
	// One budget, three per-part loops. Written once so a fourth loop cannot be
	// added without it — the shape of finding 19 and finding 26 both.
	overBudget := func() bool {
		examined++
		return examined > maxHeadersExamined
	}
	gateKnown, gate := false, false
	couldName := func() bool {
		if !gateKnown {
			gate, gateKnown = couldNameE2EEPart(reqBody), true
		}
		return gate
	}

	reader := multipart.NewReader(bytes.NewReader(reqBody), boundary)
	for {
		part, err := reader.NextPart()
		// `== io.EOF`, NOT errors.Is, is load-bearing. mime/multipart reports "no
		// more parts" as a bare io.EOF but WRAPS the failure to ever find the
		// declared boundary:
		//
		//	clean end of parts            -> "EOF"                     == io.EOF
		//	boundary never found in body  -> "multipart: NextPart: EOF" != io.EOF
		//
		// The second is a body nobody enumerated, which must reach the fail-closed
		// branch below. errors.Is matches both and would forward it — so this is
		// the rare place where the change a linter proposes reopens a hole, and
		// TestMismatchedBoundaryMentioningTheMarkerIsRefused fails when it does.
		if err == io.EOF { //nolint:errorlint // see above: errors.Is would match the wrapped boundary failure too
			return false, ""
		}
		if err != nil {
			// UNGATED too, and for the same reason: the header that broke is not
			// the one that would carry the marker. A part header without a colon
			// stops the enumeration, and every part after it — perfect,
			// readable, marker-bearing — is never seen, and a gate here is evadable
			// by the same spelling as the one above.
			//
			// But the decision is STRUCTURAL rather than unconditional, because
			// this error class is wider than "a part header is malformed": a body
			// with no closing delimiter, one truncated mid-part, and one whose
			// declared boundary never appears all arrive here too, and a client
			// that aborts an upload should not get an e2ee-flavoured 400.
			//
			// So: refuse when part-shaped content remains that this enumeration
			// never reached — a delimiter beyond the parts read plus the closing
			// one — or when the declared boundary appears nowhere, where nothing
			// was enumerated at all and a lenient parser may resolve a different
			// boundary. Measured across the whole error class:
			//
			//	bad part header, then more parts   -> delims 3, read 0  refuse
			//	bad part header, then the close    -> delims 2, read 0  refuse
			//	declared boundary never appears,
			//	  body has `--other` lines         -> delims 0, read 0  refuse
			//	  body is JSON, no `--` line       -> delims 0, read 0  forward
			//	no closing delimiter               -> delims 1, read 1  forward
			//	truncated mid part body            -> delims 2, read 2  forward
			//	truncated mid part header          -> delims 2, read 1  forward
			//
			// The last row agrees with the io.EOF case above for the same reason:
			// a header that never terminates has no body after it to hold an
			// envelope.
			delims := countLineStartsWith(reqBody, "--"+boundary)
			if delims > partsRead+1 || (delims == 0 && declaresUnreadableParts(reqBody)) {
				return true, fmt.Sprintf("the body could not be parsed as multipart (%v) and declares part-shaped content this enumeration never reached, so a smuggled envelope cannot be ruled out", err)
			}
			return false, ""
		}
		partsRead++
		if overBudget() {
			// UNGATED: the premise is that a name past the budget is never read,
			// so the one spelling the gate cannot see is the one that would evade
			// it — an RFC 2047 encoded word was forwarded from here while the
			// isEncodedWord branch refused the same name inside the budget.
			// Widening the gate instead is worse: `=?` occurs ~516 times by chance
			// in 32 MiB of random bytes, which makes it always-true.
			//
			// Costs nothing real — no client on these endpoints sends thousands of
			// parts — and it counts HEADERS, not just parts, because the inner
			// loops pay a ParseMediaType and a declaresEncodedName walk per
			// declared header: 35x at 32 MiB against one legitimate part.
			return true, fmt.Sprintf("the body declares more than %d parts or part headers, so they cannot all be enumerated from here", maxHeadersExamined)
		}
		// EVERY value, not Header.Get's first: a part may declare the header twice,
		// innocuous first and marker second, and parsers disagree about which wins
		// (Go and Python's email take the first, others the last). The contract
		// does not depend on the answer — the body demonstrably declares the name.
		// Values returns empty for a part with no disposition, so no separate skip.
		for _, disposition := range part.Header.Values("Content-Disposition") {
			if overBudget() {
				return true, fmt.Sprintf("the body declares more than %d parts or part headers, so they cannot all be enumerated from here", maxHeadersExamined)
			}
			_, dparams, derr := mime.ParseMediaType(disposition)
			if derr != nil {
				// Gated, unlike the budget and nested branches, but on the header
				// as well as the body — see mentionsEncodedWord. Making the
				// marker-bearing disposition unparseable does NOT reduce its name
				// to a guess: the marker can still be spelled in it as an encoded
				// word, which is the one spelling couldNameE2EEPart cannot see.
				//
				// Ungated entirely, this refuses any part whose disposition merely
				// fails to parse — `filename="my"file.txt"`, an unescaped quote,
				// sloppy but real and forwarded by every other path here.
				if couldName() || mentionsEncodedWord(disposition) {
					return true, "the body could be naming the reserved marker and a part's Content-Disposition could not be parsed, so the name it declares cannot be ruled out"
				}
				continue
			}
			if refuse, why := nameVerdict("a part's Content-Disposition", disposition, dparams["name"]); refuse {
				return true, why
			}
		}
		for _, partType := range part.Header.Values("Content-Type") {
			if overBudget() {
				return true, fmt.Sprintf("the body declares more than %d parts or part headers, so they cannot all be enumerated from here", maxHeadersExamined)
			}
			// A part that is ITSELF multipart hides its own parts: mime/multipart
			// does not descend, so an inner part named `_e2ee` parses cleanly and
			// is forwarded. Closed without recursion, because "the parts were not
			// enumerated" is already the condition this function fails closed on —
			// and descending would multiply the enumeration cost by the nesting
			// depth, wanting its own bound. UNGATED for the reason at the budget
			// branch above.
			//
			// Not quite free: `multipart/mixed` in a form part is the RFC 1867
			// multi-file field, dropped by HTML5 and emitted by no
			// OpenAI-compatible client, but this is a hard 400 on a shape every
			// other path forwards. Release notes, not just here.
			//
			// The other unenumerated content is NOT closed. mime/multipart is
			// symmetric about the delimiters — it skips everything before the
			// first and stops at the close — so a part-shaped block in the
			// preamble or the epilogue is never read here. Both are left open
			// deliberately: RFC 2046 §5.1.1 says both MUST be ignored, so reaching
			// either needs an upstream honouring neither delimiter, where
			// descending into a nested part is what a lenient parser does anyway.
			if looksMultipart(partType) {
				return true, "a part declares a nested multipart body, whose own parts cannot be enumerated from here"
			}
			// RFC 2045 §5 puts a `name` on Content-Type too, javax.mail's
			// getFileName() reads it as a fallback, and a part may declare it with
			// NO Content-Disposition — in which case the loop above never runs and
			// every check in it is skipped. So the same three readings apply here,
			// and not only when a disposition is absent: which header a lenient
			// parser prefers is not ours to assume. The parse failure is gated as
			// the disposition's is. One ParseMediaType per part Content-Type,
			// against the prefix test alone above; the budget already covers it.
			_, tparams, terr := mime.ParseMediaType(partType)
			if terr != nil {
				if couldName() || mentionsEncodedWord(partType) {
					return true, "the body could be naming the reserved marker and a part's Content-Type could not be parsed, so the name it declares cannot be ruled out"
				}
				continue
			}
			if refuse, why := nameVerdict("a part's Content-Type (RFC 2045 §5)", partType, tparams["name"]); refuse {
				return true, why
			}
		}
	}
}

// IsSealedRequest reports whether reqBody is a sealed envelope (SPEC §5): a JSON
// object with a top-level "_e2ee" key. It is the same test MaybeUnsealRequest
// makes before committing to fail-closed, exposed for entry points that cannot
// SERVE a sealed request and so must refuse it rather than forward it.
//
// The async submit routes are those entry points. They do not go through the
// proxy, so they never reach MaybeUnsealRequest. Without this refusal a sealed
// envelope POSTed to /v1/async/images/generations is enqueued verbatim, has its
// cleartext rewritten by forceB64ResponseFormat (which invalidates the AAD), is
// forwarded upstream still sealed, and has its result served in plaintext —
// while the user is billed for the garbage job. Little is disclosed, since the
// prompt stays sealed throughout; what breaks is that "a sealed request is
// fail-closed" becomes a property of which route the client picked rather than
// of the enclave.
//
// It takes the request's Content-Type because a sealed envelope has two ways in
// and only one of them is JSON. The async image-editing route accepts
// multipart/form-data (it preserves the boundary Content-Type for the upstream),
// and a JSON-only test says "not sealed" to a body carrying the envelope in a
// multipart part — the same hole, one request shape over. See
// multipartCarriesE2EEPart.
func (c *Ctrl) IsSealedRequest(contentType string, reqBody []byte) (bool, string) {
	return IsSealedRequest(contentType, reqBody)
}

// sealedVerdict is what a request's BYTES are, independent of which entry point
// is asking. Both of them classify identically and then differ only in what they
// do about it, which is the point: the sealed decision written twice is how the
// two disagreed about the same bytes (a multipart body reached the sync proxy's
// forward path while the async routes called it sealed), and how a
// `looksMultipart` short-circuit on one of them forwarded a genuine envelope in
// the clear while the other refused it.
type sealedVerdict int

const (
	// notSealed: forward it. Includes an ordinary multipart body, whose JSON
	// unmarshal simply fails.
	notSealed sealedVerdict = iota
	// smuggledIntoMultipart: a multipart request declares a part named `_e2ee`,
	// or is malformed in a way that leaves that impossible to rule out
	// (SPEC §5.3.1). Refuse — no multipart endpoint has a sealed request profile,
	// so there is nothing to open even when the envelope is genuine.
	smuggledIntoMultipart
	// jsonEnvelope: the body IS a sealed envelope, whatever Content-Type the
	// client put on it. Unseal it, fail-closed.
	jsonEnvelope
)

// classifyRequest is the one place the sealed decision is made. The returned
// wire.Request is populated only for jsonEnvelope; the string is the reason, for
// a rejection message.
//
// The multipart question comes FIRST, and that ordering is load-bearing rather
// than stylistic: a part name can be RFC 2231-encoded, so the body carries no
// literal "_e2ee", hasE2EEMarker returns false, and an early return on it would
// send the encoded case straight out for forwarding.
func classifyRequest(contentType string, reqBody []byte) (sealedVerdict, wire.Request, string) {
	if carries, why := multipartCarriesE2EEPart(contentType, reqBody); carries {
		return smuggledIntoMultipart, nil, why
	}
	// No short-circuit on looksMultipart. A body is an envelope or not by its
	// bytes; the Content-Type is the client's claim about them, and §5.3.1 read in
	// the other direction says a body that IS an envelope is one whatever label it
	// arrives under. An ordinary multipart body is handled correctly below anyway:
	// its unmarshal fails and it comes back notSealed, for one bytes.Contains
	// pass.
	if !hasE2EEMarker(reqBody) {
		return notSealed, nil, ""
	}
	var env wire.Request
	if err := json.Unmarshal(reqBody, &env); err != nil {
		return notSealed, nil, "" // not a JSON object → cannot be an envelope
	}
	if _, ok := env[e2eeBodyMarker]; !ok {
		return notSealed, nil, "" // the substring matched inside content
	}
	return jsonEnvelope, env, fmt.Sprintf("the body carries a top-level %q object", e2eeBodyMarker)
}

// IsSealedRequest is the receiver-free form, exported so a caller that has no
// Ctrl — the handler tests, which mock the rest of the interface — can ask the
// real question instead of reimplementing it.
//
// The method above forwards to it, which makes "this predicate reads no enclave
// state" structural rather than a comment. It was a comment, and the test held
// it up with `(&ctrl.Ctrl{})`: correct, but it encodes the assumption at the
// call site, where the day the predicate starts reading a field it fails as a
// nil-map panic inside an unrelated handler test rather than as a compile error
// here. The split is also what the predicate's meaning already implies — it
// answers a question about the BYTES, and the enclave's keys have no bearing on
// whether the client sent an envelope.
func IsSealedRequest(contentType string, reqBody []byte) (bool, string) {
	verdict, _, why := classifyRequest(contentType, reqBody)
	return verdict != notSealed, why
}

// MaybeUnsealRequest unseals a sealed E2EE request in-enclave and returns the
// reconstructed plaintext body to forward upstream. A request is sealed iff it
// carries a top-level "_e2ee" object (SPEC §5); any other request (including one
// that merely contains the substring "_e2ee" inside its content) is returned
// unchanged.
//
// On success for a sealed request it stashes, on the gin context, that the
// request was sealed and the client's response ephemeral key, so the response
// path seals its reply (SPEC §7). Once a request is confirmed sealed, any failure
// is returned as an error and MUST be treated as fail-closed by the caller (no
// plaintext fallback, SPEC §6) — a sealed request that cannot be opened, whose
// signer_addr is not this enclave, or whose key_id is unknown is rejected.
func (c *Ctrl) MaybeUnsealRequest(ctx *gin.Context, reqBody []byte) ([]byte, error) {
	verdict, env, why := classifyRequest(ctx.Request.Header.Get("Content-Type"), reqBody)
	switch verdict {
	case notSealed:
		return reqBody, nil
	case smuggledIntoMultipart:
		return nil, fmt.Errorf("multipart request must not carry a sealed envelope: %s. A sealed request is sent as JSON, and a body that cannot be parsed as an envelope is not thereby an unsealed body", why)
	}

	// Confirmed sealed from here on: fail-closed on any error.
	if len(c.teeService.EncPrivateKey) == 0 {
		return nil, fmt.Errorf("received a sealed request but the enclave enc key is not available")
	}
	e2ee, err := env.E2EE()
	if err != nil {
		return nil, fmt.Errorf("sealed request has a malformed %q envelope: %w", e2eeBodyMarker, err)
	}

	// Select the enc key by key_id (SPEC §6). The broker holds a single current
	// enc key; a mismatch means the client sealed to a rotated/foreign key we
	// cannot open, so reject with a clear error rather than a raw HPKE failure.
	if err := c.verifyEncKeyID(e2ee.KeyID); err != nil {
		return nil, err
	}

	// Enforce provider pinning (SPEC §5/§6): the enclave rejects a request pinned
	// to a different provider. The pin is the provider's TEE signer address
	// (renamed provider_id → signer_addr upstream in 0g-pc-e2ee #17; same value).
	// OpenRequest deliberately does not check this — the broker knows its own identity.
	if !strings.EqualFold(e2ee.SignerAddr, c.teeService.Address.Hex()) {
		return nil, fmt.Errorf("sealed request signer_addr %q does not match this enclave", e2ee.SignerAddr)
	}

	// Everything the receiver is responsible for now runs inside
	// wire.OpenRequestFor below (SPEC §12): the sealed set covers this profile's
	// payload, and the pinned cleartext field is present, correctly valued, not
	// sealed away and not declared unbound. Resolve the profile here — the
	// protocol package cannot know which endpoint this broker serves.
	surface := apiFormatForPath(ctx.Request.URL.Path)
	profile, sealable := profileForRequest(c.Service.Type, surface)
	if !sealable {
		return nil, fmt.Errorf("sealed requests are not supported for service type %q on the %q API surface", c.Service.Type, surface)
	}

	// Extract the client's response ephemeral key before opening, so the response
	// path can seal even though the field lives in the (now consumed) envelope.
	// Validate its length here, BEFORE the request is forwarded upstream: an
	// invalid key only breaks response sealing, which happens after inference has
	// already run — so a malformed key would otherwise buy free (unbilled) compute
	// and fail closed only at seal time. Reject it fail-closed pre-inference.
	clientEphPub, err := base64.RawURLEncoding.DecodeString(e2ee.ClientEphPub)
	if err != nil {
		return nil, fmt.Errorf("sealed request has invalid client_eph_pub: %w", err)
	}
	if len(clientEphPub) != clientEphPubLen {
		return nil, fmt.Errorf("sealed request client_eph_pub must be %d bytes (X25519), got %d", clientEphPubLen, len(clientEphPub))
	}
	// Length alone is not enough: a 32-byte value can still be a low-order/invalid
	// X25519 point that only fails at response-seal time (post-inference), which
	// would buy free unbilled compute. Probe HPKE setup now — fail closed here,
	// before forwarding upstream. The probe sealer is discarded; the response path
	// creates its own.
	if _, err := wire.NewResponseSealer(pccrypto.PublicKey(clientEphPub)); err != nil {
		return nil, fmt.Errorf("sealed request client_eph_pub is not a usable X25519 key: %w", err)
	}

	// §8 request binding: hash the on-wire aad‖ciphertext of the sealed request
	// NOW, while we still hold the envelope. The proxy replaces reqBody with the
	// reconstructed plaintext before forwarding upstream, so the response-signing
	// path (which runs post-inference) can no longer see these bytes. Stash the
	// 32-byte binding so signChatE2EE can combine it with the response hash.
	reqBindHash, err := proof.FrameBindingHash(env)
	if err != nil {
		return nil, fmt.Errorf("compute e2ee request binding: %w", err)
	}

	// Open (verifies v/kem_id, recomputes AAD, HPKE-Open fail-closed, checks
	// decrypted keys == sealed_fields with no cleartext collision, and reconstructs
	// the original request = cleartext ∪ decrypted). SPEC §6.
	reconstructed, err := wire.OpenRequestFor(profile, c.teeService.EncPrivateKey, env)
	if err != nil {
		return nil, fmt.Errorf("unseal request: %w", err)
	}

	plaintext, err := json.Marshal(reconstructed)
	if err != nil {
		return nil, fmt.Errorf("re-encode unsealed request: %w", err)
	}

	ctx.Set(CtxKeyE2EESealed, true)
	ctx.Set(CtxKeyE2EEProfile, profile)
	ctx.Set(CtxKeyE2EEClientEphPub, pccrypto.PublicKey(clientEphPub))
	ctx.Set(CtxKeyE2EEPlaintextReq, plaintext)
	ctx.Set(CtxKeyE2EEReqBindHash, reqBindHash)
	c.logger.Debugf("E2EE: unsealed request (sealed_fields=%v, key_id=%s)", e2ee.SealedFields, e2ee.KeyID)
	return plaintext, nil
}

// profileForRequest maps the endpoint this broker serves — service type AND the
// API surface the request arrived on — to the wire profile whose rules apply to a
// sealed request on it. sealable=false means "no sealed request is acceptable
// here".
//
// The SURFACE is half the key, not decoration. One chatbot service answers on two
// of them: /v1/chat/completions (OpenAI) and /v1/messages (Anthropic), whose
// payload and response shapes differ (a top-level `system` prompt; response
// frames typed by `type`, SPEC §7.2). Keyed on the service type alone,
// an Anthropic sealed request resolved to ProfileChat, which sealed an injected
// empty `choices` while the real `content`/`delta` rode in the clear — no error
// anywhere, since the wire format is identical and the frames look plausible.
//
// The mapping is an ALLOWLIST, and deliberately so. Everything absent either has
// no envelope format yet (the multipart service types — their request is
// multipart/form-data, which cannot be an envelope, so a JSON envelope arriving
// on one would unseal into a body the upstream cannot consume) or has simply not
// been specified (video-generation, and whatever service type is added next). A
// default arm that guessed ProfileChat for those would apply the wrong rule to a
// request shape nobody has analyzed — and would do it silently for a service type
// that does not exist yet. Refusing is the honest answer, and it is what SPEC §1
// requires.
//
// That allowlist discipline extends to the SURFACE, so an UNRECOGNIZED one
// (apiFormatForPath's "") is refused on the chatbot arm rather than read as
// chat's own case. It is the likelier of the two mistakes: adding a chat route
// means adding an entry to constant.TargetRoute, and teaching apiFormatForPath
// about it is a SEPARATE edit that nothing forces — so a new surface that
// resolved "" to ProfileChat would apply chat's rules to an unanalyzed request
// shape, silently, which is the exact bug the surface key was added to fix.
// Refusing costs nothing today: every chatbot route in TargetRoute (/messages,
// /v1/messages, /chat/completions) is matched by apiFormatForPath, and an
// unsealed request never reaches here at all — the profile is resolved only
// after the envelope is confirmed.
//
// The image and multipart endpoints are not chat surfaces, so the surface is
// whatever their path happened to be and only the chatbot arm consults it.
func profileForRequest(svcType, surface string) (p wire.Profile, sealable bool) {
	switch svcType {
	case constant.ServiceTypeChatbot:
		switch surface {
		case config.APIFormatAnthropic:
			return wire.ProfileAnthropic, true
		case config.APIFormatOpenAI:
			return wire.ProfileChat, true
		default:
			return "", false
		}
	case constant.ServiceTypeTextToImage:
		return wire.ProfileImage, true
	default:
		return "", false
	}
}

// verifyEncKeyID checks that a request's key_id (base64url) selects this
// enclave's current enc key (SPEC §4.3/§6). A mismatch returns an error wrapping
// ErrE2EEKeyMismatch (→ retriable 409). The current key_id is included as a
// NON-authoritative hint (a public hash, not the key material and not a trust
// source): the client must re-fetch and verify the enc key, not trust this value.
func (c *Ctrl) verifyEncKeyID(b64KeyID string) error {
	want := base64.RawURLEncoding.EncodeToString(c.teeService.KeyID)
	if b64KeyID != want {
		return fmt.Errorf("%w: sealed request key_id %q is not the enclave's current enc key (current %q); re-fetch the enc key and re-seal", ErrE2EEKeyMismatch, b64KeyID, want)
	}
	return nil
}

// e2eeSealedRequest reports whether the current request was unsealed (so the
// response must be sealed), returning the client's response ephemeral key.
func e2eeSealedRequest(ctx *gin.Context) (pccrypto.PublicKey, bool) {
	sealed, _ := ctx.Get(CtxKeyE2EESealed)
	if b, ok := sealed.(bool); !ok || !b {
		return nil, false
	}
	v, ok := ctx.Get(CtxKeyE2EEClientEphPub)
	if !ok {
		return nil, false
	}
	pub, ok := v.(pccrypto.PublicKey)
	if !ok || len(pub) == 0 {
		return nil, false
	}
	return pub, true
}

// e2eePlaintextRequest returns the reconstructed plaintext request captured at
// unseal time, used as the request side of the §8 content binding.
func e2eePlaintextRequest(ctx *gin.Context) ([]byte, bool) {
	v, ok := ctx.Get(CtxKeyE2EEPlaintextReq)
	if !ok {
		return nil, false
	}
	b, ok := v.([]byte)
	return b, ok && len(b) > 0
}

// e2eeReqBindHash returns the §8 request binding hash (sha256 of the sealed
// request's aad‖ciphertext) captured at unseal time, used as the request half of
// the E2EE response signature.
func e2eeReqBindHash(ctx *gin.Context) ([32]byte, bool) {
	v, ok := ctx.Get(CtxKeyE2EEReqBindHash)
	if !ok {
		return [32]byte{}, false
	}
	h, ok := v.([32]byte)
	return h, ok
}

// e2eeProfile returns the wire profile the request was opened under, stashed at
// unseal time. Its absence on a sealed request means the response path ran
// without a prior unseal, which the seal paths treat as fail-closed rather than
// picking a profile of their own.
func e2eeProfile(ctx *gin.Context) (wire.Profile, bool) {
	v, ok := ctx.Get(CtxKeyE2EEProfile)
	if !ok {
		return "", false
	}
	p, ok := v.(wire.Profile)
	return p, ok && p != ""
}

// maybeSealNonStreamResponse seals a non-streaming response (SPEC §7) under the
// profile the request was opened with, when the request was sealed; otherwise it
// returns body unchanged with sealed=false. Fail-closed: when the request was
// sealed but sealing fails, it returns an error and the caller MUST NOT forward
// the plaintext body.
//
// The profile comes from the context rather than the call site. It implies the
// sealed-field list AND the profile-specific checks the sealer runs on the frame
// — for image, that it carries the cleartext `usage.output_images` the router
// bills on (§7.1) — so passing the list alone let the image path seal through the
// chat profile, which knows of no such requirement. A profile CONSTANT at the
// call site fixed that but had the same shape of flaw one level up: the chatbot
// handler serves both /v1/chat/completions and /v1/messages, so its `ProfileChat`
// literal was wrong for every sealed Anthropic request. Reading what the request
// was actually opened under is the only version of this that cannot drift.
func (c *Ctrl) maybeSealNonStreamResponse(ctx *gin.Context, body []byte) (out []byte, sealed bool, respBindHash [32]byte, err error) {
	ephPub, isSealed := e2eeSealedRequest(ctx)
	if !isSealed {
		return body, false, respBindHash, nil
	}
	profile, ok := e2eeProfile(ctx)
	if !ok {
		return nil, true, respBindHash, fmt.Errorf("seal response: the request's e2ee profile is missing from the context")
	}
	var resp wire.Response
	// A literal JSON `null` unmarshals into a nil map WITHOUT error;
	// ensureSealedFieldsPresent would then panic writing to it. Reject any
	// non-object body fail-closed.
	if uerr := json.Unmarshal(body, &resp); uerr != nil || resp == nil {
		return nil, true, respBindHash, fmt.Errorf("seal response: body is not a JSON object")
	}
	// Through the SAME preparation the streaming path uses, not a local copy of
	// it: resolved against the RESPONSE rather than the profile alone (a
	// frame-typed profile answers per frame shape, §7.2), plus the placeholders a
	// frame may legitimately omit and the failure-report rule. This path had its
	// own inlined pair of those first two steps, so when the third arrived it
	// applied to streams only and a non-streaming chat/image response with a
	// top-level `error` still shipped the message in its cleartext half — the
	// exact drift a shared step exists to prevent.
	sealedFields, err := prepareFrameForSealing(profile, resp)
	if err != nil {
		return nil, true, respBindHash, fmt.Errorf("seal response: %w", err)
	}
	// Declare model + x_0g_trace unbound so the router may rewrite/inject them
	// downstream (SPEC §5.2).
	frame, err := wire.SealResponseFor(profile, ephPub, resp, sealedFields, e2eeResponseUnboundFields...)
	if err != nil {
		return nil, true, respBindHash, fmt.Errorf("seal response: %w", err)
	}
	// §8 response binding over the exact sealed frame the client receives.
	respBindHash, err = proof.FrameBindingHash(frame)
	if err != nil {
		return nil, true, respBindHash, fmt.Errorf("seal response binding: %w", err)
	}
	out, err = json.Marshal(frame)
	if err != nil {
		return nil, true, respBindHash, fmt.Errorf("encode sealed response: %w", err)
	}
	return out, true, respBindHash, nil
}

// responseFrameSealer seals a sequence of streaming SSE frames under one HPKE
// response context (SPEC §7). Frames are sealed in order; the client opens them
// in the same order.
type responseFrameSealer struct {
	sealer *wire.ResponseSealer
	binder *proof.StreamBinder
	// profile is the one the REQUEST was opened under (read off the context, not
	// re-derived), so the stream cannot be sealed under rules that were never
	// applied to the request. What each frame must seal is then resolved from the
	// frame: a frame-typed profile (Anthropic, §7.2) answers per event shape, and
	// holding one set for the whole stream is exactly the mistake — it would seal
	// nothing on every content frame.
	profile wire.Profile
	// synthFinal is what this profile's stream is CAPPED with when an upstream
	// drops off without sending a terminal event of its own. Zero for a
	// single-shape profile, whose synthetic final frame carries only empty
	// placeholders.
	//
	// It is deliberately NOT how a terminal frame is RECOGNIZED: which shapes end
	// a stream is the profile's business (Anthropic has two — a completed turn
	// ends with `message_stop`, a failed one with `error` and no `message_stop`),
	// so that question goes to wire.IsTerminalResponseFrame. Capping and
	// recognizing coincide for a normal turn and diverge for a failed one, which
	// is why they are separate.
	synthFinal   wire.Response
	emittedFinal bool
	frameCount   int
	// droppedAfterFinal counts the frames refused by handleFrameAfterFinal's drop
	// branch. Only the first is logged as it happens; the total is reported once,
	// so a chatty upstream cannot turn one stream into unbounded log volume.
	droppedAfterFinal int
	// logger reports what was dropped: a frame this broker declines to seal but
	// does not fail the request over leaves no other trace, since the client sees
	// a complete stream either way.
	logger log.Logger
}

// newResponseFrameSealer returns a per-stream frame sealer when the request was
// sealed, or (nil, nil) when it was not (the caller then forwards plaintext).
func (c *Ctrl) newResponseFrameSealer(ctx *gin.Context) (*responseFrameSealer, error) {
	ephPub, sealed := e2eeSealedRequest(ctx)
	if !sealed {
		return nil, nil
	}
	// The profile the REQUEST was opened under, so the stream cannot be sealed
	// under rules that were never applied to the request.
	profile, ok := e2eeProfile(ctx)
	if !ok {
		return nil, fmt.Errorf("e2ee stream: the request's e2ee profile is missing from the context")
	}
	// Declare model + x_0g_trace unbound on every frame so the router may
	// rewrite/inject them into the sealed stream downstream (SPEC §5.2). The whole
	// stream shares one context, so the unbound set is fixed once here.
	s, err := wire.NewResponseSealerFor(profile, ephPub, e2eeResponseUnboundFields...)
	if err != nil {
		return nil, fmt.Errorf("set up response sealer: %w", err)
	}
	// Seed the §8 streaming binding with the request hash captured at unseal time.
	// Its absence means the response path ran without a prior unseal — fail closed
	// rather than sign a stream bound to a zero request hash.
	reqBindHash, ok := e2eeReqBindHash(ctx)
	if !ok {
		return nil, fmt.Errorf("e2ee stream: request binding hash missing from context")
	}
	// Prove now that the frame this stream would be CAPPED with is one the profile
	// can actually SEAL — by sealing it, through a throwaway HPKE context whose
	// output is discarded. The check is here rather than at EOF because EOF is the
	// one moment a failure cannot be reported: the caller can no longer answer the
	// request, so a profile whose synthetic frame does not seal would leave the
	// client a stream with no final frame, a truncation it rejects wholesale (§7).
	// Refusing at setup makes that a failed request instead, with the profile
	// named and the profile's own reason attached.
	//
	// A DRY SEAL rather than resolving the sealed set, because resolving proves
	// much less than it appears to: SealFrame also requires every declared field
	// to be present and, on a final frame, runs the profile's cleartext checks.
	// The image profile passed the resolve-only probe and then failed at EOF with
	// "sealed image response must carry cleartext usage.output_images" — its §7.1
	// requirement, which a synthesized placeholder cannot satisfy. That is the
	// honest answer for it (an image stream has no legal way to be capped), and it
	// is unreachable today since only the chatbot path streams; the point is that
	// the check now establishes the property it claims instead of a weaker one.
	synthFinal := synthFinalFrameFor(profile)
	if err := dryRunSealFinalFrame(profile, ephPub, synthFinal); err != nil {
		return nil, fmt.Errorf("e2ee stream: profile %q cannot seal the synthetic terminal frame that would cap a truncated stream: %w", profile, err)
	}
	return &responseFrameSealer{
		sealer:     s,
		binder:     proof.NewStreamBinderFromReqHash(reqBindHash),
		profile:    profile,
		synthFinal: synthFinal,
		logger:     c.logger,
	}, nil
}

// dryRunSealFinalFrame seals a copy of frame as a FINAL frame through a
// throwaway HPKE context and throws the result away, reporting only whether the
// profile accepted it. It exists so newResponseFrameSealer can establish that
// the frame it would cap a truncated stream with is actually sealable, which is
// strictly more than resolving its sealed set: SealFrame also demands every
// declared field be present and runs the profile's final-frame cleartext checks.
//
// The context is discarded rather than reused for the real stream: a sealer's
// sequence numbers and its §8 binding must cover exactly the frames the client
// receives, and this frame is never sent.
func dryRunSealFinalFrame(profile wire.Profile, clientEphPub pccrypto.PublicKey, frame wire.Response) error {
	probe, err := wire.NewResponseSealerFor(profile, clientEphPub, e2eeResponseUnboundFields...)
	if err != nil {
		return err
	}
	dry := wire.Response{}
	for k, v := range frame {
		dry[k] = v
	}
	sealedFields, err := prepareFrameForSealing(profile, dry)
	if err != nil {
		return err
	}
	_, err = probe.SealFrame(dry, sealedFields, true)
	return err
}

// prepareFrameForSealing resolves what this frame must seal and fills in the
// placeholders a frame of this profile may legitimately omit, returning the
// sealed set to hand SealFrame. Shared by the real seal path and the dry run, so
// the dry run cannot drift from what it is meant to predict.
//
// It also SEALS A FAILURE REPORT the profile's own set does not name. `error` is
// content on either surface — an upstream error message can quote the request
// that produced it — but only the Anthropic taxonomy says so: chat's sealed set
// is ["choices"] whatever the frame holds, so an OpenAI-style `{"error": …}`
// chunk mid-stream had its message ride in the frame's cleartext half, reaching
// every intermediary on an otherwise sealed turn. Adding it is legal (a sealed
// SUPERSET is permitted; only the profile's required set is mandated) and it
// opens on a conforming client, verified end to end.
//
// Only for a profile with no discriminator, because a frame-typed one already
// governs the field and disagrees about where it belongs: its `error` shape
// seals `error` itself, and carrying it on any other shape is refused outright
// ("it is generated content under some frame shape, so a frame may carry it only
// by sealing it") — while adding it to a shape that seals nothing is refused
// too ("must seal nothing"). So there the taxonomy is both sufficient and the
// only correct answer.
//
// Its reach ends at the response body: nothing here runs when the upstream
// returns a NON-200, because ProcessHTTPRequest hands that to handleServiceError
// and returns before any charging handler. Such a body reaches the client in the
// clear on a sealed turn — the same content class this rule seals, on a path
// where the taxonomy has nothing to say (an upstream 500 body is not a frame of
// any profile), so closing it needs a wire shape for an HTTP-level failure
// rather than an improvised seal here. Boundary documented in
// docs/design/e2ee.md ("An upstream HTTP error body is NOT sealed").
func prepareFrameForSealing(profile wire.Profile, frame wire.Response) ([]string, error) {
	sealedFields, err := wire.ResponseSealedFieldsForFrame(profile, frame)
	if err != nil {
		return nil, err
	}
	if !profileHasFrameDiscriminator(profile) {
		if v, ok := frame[failureField]; ok && !isEmptyJSONValue(v) && !slices.Contains(sealedFields, failureField) {
			sealedFields = append(slices.Clone(sealedFields), failureField)
		}
	}
	ensureSealedFieldsPresent(profile, frame, sealedFields)
	return sealedFields, nil
}

// profileHasFrameDiscriminator reports whether this profile's response frames
// name their own shape in a cleartext field the wire package validates — which
// is what makes an `event:` line derivable from a frame, and what makes the
// derived value trustworthy (ResponseSealedFieldsForFrame refuses a shape outside
// the taxonomy, so the value can only be one of a fixed set of identifiers).
//
// It sits beside synthFinalFrameFor because it is the same kind of per-profile
// fact — what this profile's SSE stream looks like — and, like it, is a serving
// question the wire package does not expose an answer to. Both are the reason a
// frame-typed profile added without touching this file is refused at stream
// setup rather than served wrongly.
func profileHasFrameDiscriminator(p wire.Profile) bool {
	return p == wire.ProfileAnthropic
}

// synthFinalFrameFor returns the plaintext frame to cap a truncated stream of
// this profile with, or nil for a profile whose streams have no such event.
//
// It answers with the FRAME only, no event name beside it: sealFrame derives the
// SSE `event:` line from whatever frame it is sealing, reading the frame's own
// bound discriminator, so a synthesized event is announced exactly like a
// forwarded one and the name is not a second per-profile literal anywhere.
//
// Anthropic's stream ends with a `message_stop` event rather than a `[DONE]`
// sentinel, and that event is a legal frame of the profile — it seals nothing
// (§7.2) — so it is what an upstream that dropped off should have sent, and what
// this broker sends in its place. A chat stream has no equivalent event: its
// final frame is a placeholder with empty content, so it gets the zero value.
//
// A stream that failed partway ends with `error`, which is terminal too — but a
// broker never SYNTHESIZES one: it has no error to report, only a truncation,
// and inventing an `error` frame would attribute a failure to the model that the
// model did not produce.
//
// The capped turn is INCOMPLETE, deliberately visibly so. Anthropic's grammar
// ends a turn `content_block_stop` → `message_delta` (which carries `stop_reason`
// and `usage.output_tokens`) → `message_stop`, and a stream truncated
// mid-`content_block_delta` skips the first two: an SDK accumulating it gets a
// message with `stop_reason: null`, and the router sees no output-token count.
// Filling that gap with a synthesized `message_delta` would mean inventing both
// values — and §8 signs whatever is sent, so the broker would be attesting
// numbers the model never produced. A null `stop_reason` is the honest signal
// that the turn did not complete.
//
// This is the one per-profile literal left in this file, and the profile that
// needs it is the only one that can supply it: the wire package owns which
// shapes END a stream, but "which event should a broker invent when the upstream
// sent none" is a serving decision, not a wire rule (an enclave could
// legitimately choose to fail the request instead). A frame-typed profile added
// without an entry here therefore does not degrade quietly:
// newResponseFrameSealer proves the entry can be sealed before the first frame
// goes out, so the stream is refused up front rather than truncated at EOF.
func synthFinalFrameFor(p wire.Profile) wire.Response {
	if p == wire.ProfileAnthropic {
		return wire.Response{anthropicFrameType: json.RawMessage(`"` + anthropicMessageStop + `"`)}
	}
	return nil
}

// sealSSELine transforms one already-sanitized SSE line into its sealed form
// (SPEC §7). A "data: {json}" chunk is sealed as a NON-final frame, except an
// event a frame-typed profile defines as TERMINAL (for Anthropic `message_stop`
// closing a completed turn, or `error` closing a failed one), which is sealed AS
// the final frame — it is last by definition, so nothing synthetic follows it.
//
// For a chat stream, which has no such event, exactly one final frame is emitted
// synthetically at stream end — before a "data: [DONE]" sentinel here, or on EOF
// by the caller via finalFrameLine. Deriving `final` from per-frame usage is
// deliberately avoided: some upstreams emit empty "usage":{} mid-stream, and vLLM
// continuous_usage_stats puts usage on every chunk, either of which would mark a
// non-terminal frame final and truncate the client's stream.
//
// What passes through is an ALLOWLIST, because a sealed stream's every byte
// should be either sealed or accounted for: the blank line that separates SSE
// events, the `[DONE]` sentinel where the profile's own grammar has one, and
// `data:` frames (sealed). Every other line — `event:`, `id:`, `retry:`, an
// unknown field — is DROPPED, and a `data:` payload that is not a JSON object
// fails the stream closed.
//
// The reason is the same for all of them: they sit outside the frame JSON and so
// outside the AAD and the §8 binding, and while everything a sealed frame's
// cleartext half may contain is checked by the profile taxonomy, these lines are
// checked by nothing (sanitizeStreamLine's leak-field stripping, #184, only
// inspects `data:` JSON too). Forwarding one hands an upstream a channel for
// arbitrary text to the client and to every intermediary on an otherwise sealed
// turn. The `event:` line loses nothing by being dropped, since §7.2 already
// requires a receiver to ignore the received line and rebuild it from the bound
// discriminator — which is what sealFrame does.
func (rs *responseFrameSealer) sealSSELine(line string) (string, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return line, nil // preserve SSE event separators
	}
	after, ok := strings.CutPrefix(trimmed, "data:")
	if !ok {
		// Dropping is only correct for a line that IS an SSE field line. A line that
		// is not one means the upstream answered a STREAMING request with something
		// that is not an SSE stream at all — in practice a whole non-streaming JSON
		// body from a provider that ignored `stream: true`. Discarding that as if it
		// were an `id:` line throws away the ENTIRE answer, and the stream is then
		// capped with a synthetic terminal frame: the client receives a sealed,
		// signed, well-formed turn that reports as normally completed and contains
		// nothing, while billing reads the same bytes the client never got. So it
		// fails closed, exactly like the non-object `data:` payload below — on a
		// sealed turn plaintext cannot be forwarded, and silence must not be
		// reported as success.
		if !isSSEFieldLine(trimmed) {
			return "", fmt.Errorf("seal stream frame: upstream answered a streaming request with %s (%d bytes) rather than an SSE stream, so the response cannot be sealed", nonSSELineKind(trimmed), len(trimmed))
		}
		// A real SSE field line: `event:` (rebuilt by sealFrame from the bound
		// discriminator), `id:`, `retry:`, an SSE comment, or an unrecognized field
		// name the SSE spec has receivers ignore. None of them is inside the AAD,
		// none of them is checked by anything, and none carries content a sealed
		// receiver may act on — so none is forwarded. Debug rather than Warn: an
		// upstream that sends `id:` sends it on every frame, and this is a normal
		// thing to discard, not an incident.
		//
		// The NAME is logged, never the value: the name is a token by construction
		// (isSSEFieldLine just validated it), while the value is upstream-controlled
		// text of any length — the place a credential or a megabyte would sit.
		name, _, _ := strings.Cut(trimmed, ":")
		rs.logger.Debugf("e2ee stream: dropping a non-data SSE line (field %q, %d bytes), which no sealed receiver may trust", name, len(trimmed))
		return "", nil
	}
	// The sentinel is recognized from the PARSED payload, so the space after the
	// colon — which SSE makes OPTIONAL — cannot change the outcome. It used to be
	// matched against the raw line (`isStreamDone`, still used by the plaintext
	// path), which meant `data:[DONE]` fell through to the fail-closed branch
	// below and destroyed a turn that had already delivered every content frame:
	// the client got a stream with no final frame, which §7 makes it reject
	// wholesale, plus a JSON error body behind it.
	payload := strings.TrimSpace(after)
	if payload == doneSentinel {
		final, err := rs.finalFrameLine()
		if err != nil {
			return "", err
		}
		if profileHasFrameDiscriminator(rs.profile) {
			// Not part of this profile's stream grammar: `[DONE]` is an OpenAI
			// sentinel, and a Messages stream ends with a terminal EVENT instead. It
			// arrives with no `event:` line on the one profile where this broker
			// rebuilds every such line from the bound discriminator, and behind the
			// final frame it is exactly what handleFrameAfterFinal drops — a
			// trailing `ping` carries strictly more and is dropped with a Warn. So
			// dropping it is what makes the allowlist consistent; forwarding it left
			// one line outside the AAD and the §8 binding. Reachable: LiteLLM's
			// Anthropic passthrough emits it on some versions.
			//
			// finalFrameLine still runs first, and that is not a contradiction: the
			// sentinel is not part of the GRAMMAR but it is still a reliable
			// end-of-stream signal, and in the case that actually occurs the terminal
			// event came first, so it returns "" and nothing is synthesized. It
			// matters only for an upstream that sends the sentinel and no terminal
			// event, where capping here rather than at EOF is the same frame a moment
			// earlier.
			rs.logger.Debugf("e2ee stream: dropping a %s sentinel, which is not part of the %q stream grammar", doneSentinel, rs.profile)
			return final, nil
		}
		return final + line, nil // synthetic final frame (if any) precedes [DONE]
	}
	if payload == "" {
		// A `data:` line with no payload carries no frame and no content, so there
		// is nothing to seal and nothing to leak. Dropped like any other line a
		// sealed receiver cannot act on.
		rs.logger.Debugf("e2ee stream: dropping an empty `data:` line")
		return "", nil
	}
	if !strings.HasPrefix(payload, "{") {
		// Neither the sentinel nor a JSON object, so there is no frame to seal and
		// nothing that could check it — the same hole as a forwarded `event:`
		// line, one branch away, except that clients RENDER `data:` payloads. A
		// sealed stream fails closed rather than passing arbitrary text through to
		// the client and every intermediary in the clear.
		return "", fmt.Errorf("seal stream frame: upstream sent a `data:` payload that is neither %s nor a JSON object, so it cannot be sealed", doneSentinel)
	}
	var frame wire.Response
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return "", fmt.Errorf("seal stream frame: %w", err)
	}
	// §7 puts the final frame LAST, so a frame arriving behind it is never
	// SEALED. Two things would otherwise go wrong, and the second is the serious
	// one: it would be sealed final=false behind a frame that already said final
	// (no receiver-side check catches that — OpenFrame has no post-final rule),
	// and sealFrame would fold it into the §8 streaming binding. A client stops
	// consuming at the frame marked final, so it would recompute the binding over
	// N frames while this broker signed N+1, and the signature would fail to
	// verify on a turn that otherwise succeeded.
	//
	// Handling it HERE, before sealFrame, is what keeps the binding equal to what
	// the client received.
	//
	// This became reachable with frame-typed profiles: a chat stream can only
	// emit its final frame at [DONE] or EOF, i.e. where nothing follows, but an
	// Anthropic terminal event (`error`, or a `message_stop` an upstream appends
	// anything to) can land mid-stream.
	if rs.emittedFinal {
		return rs.handleFrameAfterFinal(frame)
	}
	return rs.sealFrame(frame, rs.isTerminal(frame))
}

// isSSEFieldLine reports whether a line is an SSE field line: an SSE comment (a
// leading colon, i.e. an empty field name) or a field-NAME TOKEN, optionally
// followed by ":" and a value.
//
// The token rule is what separates a line a sealed stream may silently DISCARD
// from one it must refuse. "Has a colon" cannot do it: a bare JSON body's first
// colon makes `{"type"` look like a field name, which is how a whole
// non-streaming response passed for an `id:` line and was dropped. A real field
// name is a token, so anything with a brace, a quote or a space in it is not a
// field line at all but a body the upstream sent instead of a stream.
//
// An unrecognized NAME still counts: the SSE spec has receivers ignore unknown
// fields, so an upstream may legitimately send one, and dropping it is safe —
// nothing is forwarded and nothing is lost, because a field this broker does not
// know is not where an answer lives.
func isSSEFieldLine(trimmed string) bool {
	if strings.HasPrefix(trimmed, ":") {
		return true // an SSE comment (typically a keepalive): no field, no content
	}
	name, _, _ := strings.Cut(trimmed, ":")
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			continue
		}
		return false
	}
	return true
}

// nonSSELineKind names what a non-SSE line looks like WITHOUT quoting any of it.
// The returned strings are this broker's own, so the error they end up in — which
// reaches the client — carries no upstream-controlled text; the line's length is
// reported separately and is enough to tell a stray byte from a whole body.
func nonSSELineKind(trimmed string) string {
	switch {
	case strings.HasPrefix(trimmed, "{"), strings.HasPrefix(trimmed, "["):
		return "a bare JSON body"
	case strings.HasPrefix(trimmed, "<"):
		return "a markup document"
	default:
		return "free-form text"
	}
}

// handleFrameAfterFinal decides what to do with a data frame that arrived behind
// the final one. Either way it is not sealed and not bound; the question is only
// whether the stream continues.
//
// It is DROPPED when the frame CARRIES no answer: a duplicate or trailing
// `message_stop`, a `ping`, or any frame holding none of the fields its shape
// would seal — EMPTY counting as absent, since `choices: []` is how this file
// itself writes "nothing here" (ensureSealedFieldsPresent manufactures exactly
// that as its placeholder). OpenAI's trailing usage-only chunk is the case that
// makes the distinction load-bearing: it carries `"choices": []`, so a
// presence-only test failed the very frame this branch was written for.
// That is the case actually seen in the wild — a proxy that appends
// `message_stop` after `error` or sends it twice, and a chat upstream's trailing
// usage-only chunk behind [DONE] — and the client is unharmed, having already
// received a complete final frame. Failing instead would be worse than the
// quirk: the stream is already committed and flushed, so the error path appends
// a JSON error body behind the sealed final frame and reports a turn that fully
// delivered as a broker error.
//
// It FAILS the stream when the frame does carry one of those fields, when it
// reports a FAILURE (a non-empty `error`, which is content on either surface
// even where the profile's sealed set does not name it), or when its shape is
// unknown and so might carry either. That is the one case where dropping loses data:
// something the client will never see, silently. Stopping is also all this
// broker can do about it — the frame cannot be sealed without breaking the §8
// binding the client verifies. Being TERMINAL is not an exemption: Anthropic's
// `error` is terminal AND carries content, so a trailing one reports a real
// downstream failure and must not be swallowed — and neither may chat's, which
// is why the failure check does not go through the sealed set.
//
// The decision is on what the frame HOLDS, not on what its shape may seal,
// because for a single-shape profile those differ: chat's sealed set is
// ["choices"] for every frame whatever it contains, and no chat frame is ever
// terminal, so a shape-based test failed the stream on every post-[DONE] chunk —
// including the usage-only one that legitimately carries no `choices` at all
// (the frame ensureSealedFieldsPresent exists to accommodate). For a frame-typed
// profile the two tests agree: a `content_block_delta` carries its `delta`, and
// `ping` / `message_stop` carry nothing to begin with.
func (rs *responseFrameSealer) handleFrameAfterFinal(frame wire.Response) (string, error) {
	const because = "§7 requires the final frame to be last, and sealing this one would break the §8 binding the client recomputes"
	sealed, err := wire.ResponseSealedFieldsForFrame(rs.profile, frame)
	if err != nil {
		return "", fmt.Errorf("seal stream frame: upstream sent a frame of unknown shape after the terminal frame, which may carry content: %s: %w", because, err)
	}
	for _, f := range sealed {
		v, ok := frame[f]
		if !ok || isEmptyJSONValue(v) {
			continue
		}
		// Carries an answer. Being TERMINAL does not exempt it: Anthropic's
		// `error` is both terminal and content-bearing, so a "terminal frames are
		// safe to drop" shortcut silently swallowed a downstream failure report
		// that arrived behind a `message_stop` — the exact case this branch exists
		// for. Whether a shape ends a stream says nothing about whether it carries
		// something the client needs.
		return "", fmt.Errorf("seal stream frame: upstream sent a frame (%s) carrying %q after the terminal frame: %s", frameDescriptionOf(frame, rs.profile), f, because)
	}
	// A failure REPORT is content too, whatever the profile's sealed set says.
	// The sealed-set loop above catches Anthropic's, because `error` is that
	// profile's content field for the `error` shape — but chat's sealed set is
	// only ["choices"], so an OpenAI-style `{"error": …}` behind [DONE] fell
	// through to the drop below and the client saw a normally-completed turn. The
	// asymmetry was accidental (a vocabulary difference, not a decision), and it
	// lands on exactly the frame this branch exists to protect: the one telling
	// the caller its turn did not really succeed.
	if errField, ok := frame[failureField]; ok && !isEmptyJSONValue(errField) {
		return "", fmt.Errorf("seal stream frame: upstream sent a frame (%s) carrying %q after the terminal frame: %s", frameDescriptionOf(frame, rs.profile), failureField, because)
	}
	// One Warn per stream, not per frame: the actionable signal is "this upstream
	// sends frames after the terminal one", which the first occurrence carries in
	// full. The rest are counted and reported once by logDroppedAfterFinal, so an
	// upstream emitting many of them cannot amplify into unbounded log volume —
	// this path returns ("", nil) and the caller keeps reading.
	rs.droppedAfterFinal++
	if rs.droppedAfterFinal == 1 {
		rs.logger.Warnf("e2ee stream: dropping a frame (%s) that arrived after the terminal frame and carries no answer; %s", frameDescriptionOf(frame, rs.profile), because)
	}
	return "", nil
}

// logDroppedAfterFinal reports, once, how many frames this stream dropped behind
// its terminal frame. Called by the caller after the stream loop, beside
// signedText: the first drop is logged as it happens, and this is what keeps the
// rest from being either silent or unbounded.
func (rs *responseFrameSealer) logDroppedAfterFinal() {
	if rs.droppedAfterFinal > 1 {
		rs.logger.Warnf("e2ee stream: dropped %d frames that arrived after the terminal frame (only the first is logged in full)", rs.droppedAfterFinal)
	}
}

// isEmptyJSONValue reports whether a raw JSON value carries nothing: `null`, an
// empty array, object or string. It is the counterpart of the empty-array
// placeholder ensureSealedFieldsPresent injects — a field holding `[]` and a
// field that is absent mean the same thing on this wire, so they must take the
// same branch wherever "does this frame carry an answer" is asked.
func isEmptyJSONValue(v json.RawMessage) bool {
	var decoded any
	if err := json.Unmarshal(v, &decoded); err != nil {
		return false // undecodable: treat as content rather than assume it is empty
	}
	switch t := decoded.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	case string:
		return t == ""
	}
	return false
}

// frameKindOf returns a frame's bound discriminator value, or "" when it has
// none (a single-shape profile's frames, which their API sends without an
// `event:` line). It reads the same cleartext field the wire package keys its
// per-shape rules off, which is why the line built from it is trustworthy in a
// way the upstream's own line is not: this value is inside the AAD.
func frameKindOf(frame wire.Response) string {
	var kind string
	if err := json.Unmarshal(frame[anthropicFrameType], &kind); err != nil {
		return ""
	}
	return kind
}

// frameDescriptionOf names a frame for a log or an error: its shape for a
// frame-typed profile, and just the profile for a single-shape one, whose frames
// have no shape to name. It is for humans only — every decision reads the shape
// through the wire package.
//
// Gated on the same predicate as the `event:` line, and for the same reason. On a
// single-shape profile `type` is an ordinary cleartext field the wire package has
// no rule about, so it is arbitrary, unbounded upstream text — and these strings
// reach a broker log line and, on the fail path, an error that
// handleBrokerError hands to the CLIENT. That let an upstream write forged lines
// into the log ("chat FORGED\nERROR broker: …") and choose the text of a
// broker-attributed error. On a frame-typed profile the value is already
// validated: ResponseSealedFieldsForFrame refuses any shape outside the taxonomy
// before either caller reaches here.
//
// The gate is also the more accurate description: on chat a `type` field is not
// a shape name at all.
func frameDescriptionOf(frame wire.Response, profile wire.Profile) string {
	if profileHasFrameDiscriminator(profile) {
		if kind := frameKindOf(frame); kind != "" {
			return fmt.Sprintf("%s %s", profile, kind)
		}
	}
	return fmt.Sprintf("%s profile", profile)
}

// isTerminal reports whether this frame is an event that CLOSES this profile's
// stream, and so must be sealed with final=true.
//
// Which shapes those are is the profile's business, so it is asked, not
// hardcoded here: Anthropic has two — `message_stop` for a completed turn and
// `error` for one that failed partway, which sends no `message_stop` at all.
// Recognizing only the frame this broker would SYNTHESIZE (see synthFinalFrame)
// would mark an error-terminated stream non-final, and the EOF path would then
// append a `message_stop` after the `error` — a sequence no Anthropic stream
// produces, reading to a client as a turn that completed normally.
//
// A single-shape profile has no terminal event and answers false for every
// frame, which is what keeps the chat stream on its synthetic-final path. An
// unrecognized shape also answers false; sealFrame refuses it a moment later,
// which is where that belongs.
//
// It has no opinion about a SECOND terminal event, deliberately: nothing reaches
// it once one has been sealed, because sealSSELine refuses every data frame
// behind the final one. `final` appearing exactly once is that rule's job, not a
// duplicate check here.
func (rs *responseFrameSealer) isTerminal(frame wire.Response) bool {
	terminal, err := wire.IsTerminalResponseFrame(rs.profile, frame)
	return err == nil && terminal
}

// finalFrameLine returns a synthetic final SSE frame so the client always
// receives exactly one completion marker (SPEC §7). It returns "" if a final
// frame was already emitted, making it safe to call on both [DONE] and EOF — and
// on an Anthropic stream that already sent a terminal event, which IS the final
// frame, so the EOF path then adds nothing.
//
// What the frame contains comes from the profile, never a literal
// {"choices": []}: a single-shape profile gets empty placeholders for its sealed
// fields (every one of them is a JSON array, so an empty one merges to nothing on
// the client), and a frame-typed profile gets the event a healthy upstream would
// have closed with, which is a legal frame of that profile and seals nothing.
func (rs *responseFrameSealer) finalFrameLine() (string, error) {
	if rs.emittedFinal {
		return "", nil
	}
	frame := wire.Response{}
	for k, v := range rs.synthFinal {
		frame[k] = v
	}
	// sealFrame builds the `event:` line from this frame's own bound `type`, so a
	// synthesized event is announced exactly like a forwarded one.
	return rs.sealFrame(frame, true)
}

// sealFrame seals one frame object and returns a self-contained SSE event:
// "data: {json}\n\n". The trailing blank line is the SSE event terminator and is
// REQUIRED — the client's SSE reader concatenates consecutive "data:" lines that
// are not separated by a blank line into a single event, so without it a sealed
// frame would merge with the following frame or the "data: [DONE]" sentinel,
// yielding "{json}\n[DONE]" and a JSON decode error on the client. We terminate
// every frame ourselves rather than relying on the upstream's blank line (which
// an abrupt EOF may omit); an extra blank line the upstream also sends is
// harmless (ignored by SSE parsers).
func (rs *responseFrameSealer) sealFrame(frame wire.Response, final bool) (string, error) {
	// Resolved per frame, not once per stream: a frame-typed profile's answer is a
	// property of the frame (§7.2), and one set held for the whole stream would
	// seal nothing on every content frame.
	sealedFields, err := prepareFrameForSealing(rs.profile, frame)
	if err != nil {
		return "", fmt.Errorf("seal frame: %w", err)
	}
	out, err := rs.sealer.SealFrame(frame, sealedFields, final)
	if err != nil {
		return "", fmt.Errorf("seal frame: %w", err)
	}
	// The SSE `event:` line, rebuilt from the frame's own BOUND discriminator —
	// the upstream's was dropped (see sealSSELine), and this is the same
	// derivation §7.2 requires of a receiver.
	//
	// Only for a profile that HAS a discriminator, which is the load-bearing half.
	// On such a profile the value is already validated: ResponseSealedFieldsForFrame
	// above refuses any shape outside the taxonomy, so `kind` is one of a fixed set
	// of identifiers. On a single-shape profile nothing validates it — `type` is an
	// ordinary cleartext field the wire package has no rule about — so an upstream
	// could put anything there, INCLUDING a newline, and a line built from it would
	// end and start a fresh SSE line: an attacker-chosen, unsealed, unbound `data:`
	// frame written into a sealed stream, ahead of the real one. That is exactly the
	// channel dropping the upstream's own `event:` line closes, so it must not be
	// reopened here.
	eventLine := ""
	if profileHasFrameDiscriminator(rs.profile) {
		kind := frameKindOf(frame)
		if strings.ContainsAny(kind, "\r\n") {
			// Unreachable through the taxonomy, and fail-closed rather than
			// silently dropped: a shape identifier with a line break means an
			// assumption above this line stopped holding.
			return "", fmt.Errorf("seal frame: frame discriminator %q contains a line break", kind)
		}
		if kind != "" {
			eventLine = "event: " + kind + "\n"
		}
	}
	// Fold the exact on-wire frame into the §8 streaming binding, in send order
	// (the final frame last), so the signed aggregate matches what the client
	// recomputes over the frames it receives.
	if err := rs.binder.AddFrame(out); err != nil {
		return "", fmt.Errorf("bind sealed frame: %w", err)
	}
	rs.frameCount++
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encode sealed frame: %w", err)
	}
	if final {
		rs.emittedFinal = true
	}
	return eventLine + "data: " + string(b) + "\n\n", nil
}

// signedText finalizes the §8 streaming binding and returns the scheme-tagged
// signed text (proof.StreamBinder.Text). ok is false when no frame was sealed (a
// degenerate empty stream), so the caller skips caching a signature rather than
// binding sha256("").
func (rs *responseFrameSealer) signedText() (text string, ok bool, err error) {
	if rs.frameCount == 0 {
		return "", false, nil
	}
	text, err = rs.binder.Text()
	return text, err == nil, err
}

// placeholderSealedFields are, PER PROFILE, the sealed fields a legitimate frame
// of that profile can OMIT and whose value is a JSON array, so that an empty one
// merges to nothing on the client. Both halves must hold to be listed, and the
// first is the operative one: the placeholder is for a frame that is well-formed
// WITHOUT the field, never for one that should have carried it.
//
// Keyed on the profile because that is what the invariant is a property of — the
// field NAME alone does not carry it. A bare name set is correct only by
// coincidence of vocabulary: a frame-typed profile with a shape whose content
// field happened to be called `data` would inherit the image profile's
// permission and get a placeholder on a frame obliged to carry content, which is
// exactly the failure the Anthropic reasoning below rejects.
//
// Only chat and image have an entry. A trailing usage-only chat chunk
// legitimately carries no `choices`, and that is what this exists for.
//
// Anthropic has NO entry, for two separate reasons:
//   - its per-shape stream fields (`delta`, `content_block`, `error`) are OBJECTS,
//     where `[]` would not be a placeholder but a type error shipped to a client;
//   - its non-streaming `content` IS an array, but the Messages API always returns
//     it on a `message` response (an empty array at worst), so a placeholder there
//     could only ever fire on a broken upstream — and it would then seal, sign and
//     mark final a frame containing an empty answer, while the router bills the
//     output tokens the same response reported. Nothing would report a problem:
//     exactly the silent failure this profile's rules exist to remove.
//
// So a frame whose shape declares a field it does not carry fails closed here,
// with the sealer's own "sealed field not present in frame".
var placeholderSealedFields = map[wire.Profile]map[string]struct{}{
	wire.ProfileChat:  {"choices": {}},
	wire.ProfileImage: {"data": {}},
}

// ensureSealedFieldsPresent guarantees every field of sealedFields that a frame
// of THIS profile may legitimately omit (placeholderSealedFields) exists on it,
// so SealFrame — which errors on a declared-but-absent sealed field — never
// fails on a frame that is well-formed without one, e.g. a trailing usage-only
// chat chunk with no "choices".
//
// The profile travels with the field list because the list alone cannot answer
// the question; both callers resolved the list FROM a profile, so neither has to
// go looking for it.
//
// This is a shape guard, not a content fallback: a path where a missing sealed
// field would mean lost content must reject BEFORE sealing rather than rely on
// this (the image path refuses an undecodable response for exactly that reason).
// Anything not listed for this profile is left alone, so the sealer's own
// "sealed field not present in frame" is the answer for it.
func ensureSealedFieldsPresent(profile wire.Profile, frame wire.Response, sealedFields []string) {
	mayOmit := placeholderSealedFields[profile]
	for _, f := range sealedFields {
		if _, ok := mayOmit[f]; !ok {
			continue
		}
		if _, ok := frame[f]; !ok {
			frame[f] = json.RawMessage("[]")
		}
	}
}
