package main

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

// renderCompose renders the compose template the way generateDockerCompose does, without
// writing a file.
func renderCompose(t *testing.T, data TemplateData) string {
	t.Helper()

	tmpl, err := template.New("dockercompose").Parse(dockerComposeTemplate)
	if err != nil {
		t.Fatalf("parsing the compose template: %v", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("rendering the compose template: %v", err)
	}
	return out.String()
}

// phalaData is a deployment on a real TEE node, which is the only configuration where any of
// the sockets exist.
func phalaData(useController bool) TemplateData {
	repo, digest := splitPinnedImage(brokerImage)
	return TemplateData{
		TeeNode:          "phala",
		UseController:    useController,
		ConfigPath:       "./config.yml",
		AttestSocketDir:  attestSocketDir,
		AttestSocketPath: attestSocketDir + "/tee.sock",
		ImageRepo:        repo,
		ImageDigest:      digest,
	}
}

// serviceBlock returns the lines belonging to one service, so an assertion about "the broker
// mounts X" cannot be satisfied by some other service mounting it.
func serviceBlock(t *testing.T, compose, service string) string {
	t.Helper()

	lines := strings.Split(compose, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == service+":" && strings.HasPrefix(line, "  "+service) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no service %q in the generated compose", service)
	}
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		// The next service, or the end of the services section.
		if trimmed != "" && !strings.HasPrefix(lines[i], "   ") && !strings.HasPrefix(lines[i], "  #") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// Without a controller nothing changes. This is the compatibility half, and it is the reason
// every existing provider is unaffected: the deployment it gets is the one it got before.
func TestWithoutAControllerTheDeploymentIsUnchanged(t *testing.T) {
	broker := serviceBlock(t, renderCompose(t, phalaData(false)), "0g-serving-provider-broker")

	for _, want := range []string{
		"/var/run/dstack.sock:/var/run/dstack.sock",
		"/var/run/docker.sock:/var/run/docker.sock",
	} {
		if !strings.Contains(broker, want) {
			t.Errorf("the broker is missing %q, which a controller-less deployment still has", want)
		}
	}
	for _, unwanted := range []string{"TEE_SOCKET", "IMAGE_REPO", "IMAGE_DIGEST", attestSocketDir} {
		if strings.Contains(broker, unwanted) {
			t.Errorf("the broker carries %q with no controller deployed", unwanted)
		}
	}
}

// With a controller, the broker holds neither socket. This is the only assertion in this file
// that pins a property rather than a description: every link in
// doc/attestation-trust-chain.md rests on the broker being unable to write RTMR3, derive a
// key, or recreate a container, and all three are properties of these mounts being absent.
func TestWithAControllerTheBrokerHoldsNeitherSocket(t *testing.T) {
	compose := renderCompose(t, phalaData(true))

	for _, service := range []string{"0g-serving-provider-broker", "0g-serving-provider-event"} {
		block := serviceBlock(t, compose, service)
		if strings.Contains(block, "/var/run/dstack.sock") {
			t.Errorf("%s mounts dstack's socket: it could append any RTMR3 record about itself and derive any image's signing key", service)
		}
		if strings.Contains(block, "/var/run/docker.sock") {
			t.Errorf("%s mounts docker's socket: it could recreate any container, so this file cannot say who may change what", service)
		}
		if !strings.Contains(block, "TEE_SOCKET="+attestSocketDir+"/tee.sock") {
			t.Errorf("%s has no TEE_SOCKET, so it would look for dstack's socket and find nothing", service)
		}
		if !strings.Contains(block, "zg-tee:"+attestSocketDir) {
			t.Errorf("%s does not mount the attestation socket volume, so it cannot reach the controller", service)
		}
	}
}

// The controller is the one container that gets dstack's socket, and it must serve the proxy
// the broker was just pointed at.
func TestWithAControllerOnlyTheControllerHoldsDstack(t *testing.T) {
	compose := renderCompose(t, phalaData(true))
	controller := serviceBlock(t, compose, "0g-controller")

	for _, want := range []string{
		"/var/run/dstack.sock:/var/run/dstack.sock",
		"ATTEST_PROXY_SOCKET=" + attestSocketDir + "/tee.sock",
		"zg-tee:" + attestSocketDir,
	} {
		if !strings.Contains(controller, want) {
			t.Errorf("the controller is missing %q", want)
		}
	}

	// Exactly one service may hold it.
	if n := strings.Count(compose, "/var/run/dstack.sock:/var/run/dstack.sock"); n != 1 {
		t.Errorf("%d services mount dstack's socket, want only the controller", n)
	}

	// And the volume carrying the signing oracle is declared, since anything that can reach
	// that socket can have the broker's key applied to a hash of its choosing.
	if !strings.Contains(compose, "\n  zg-tee:") {
		t.Error("the zg-tee volume is not declared")
	}
	if n := strings.Count(compose, "zg-tee:"+attestSocketDir); n != 3 {
		t.Errorf("%d services mount zg-tee, want exactly the broker, the event service and the controller", n)
	}
}

// The controller must not wait for the broker to be healthy.
//
// A hardened broker cannot finish starting until the controller answers /SignerAddress, so
// the two would wait for each other and the deployment would never come up.
func TestTheControllerDoesNotWaitForTheBroker(t *testing.T) {
	controller := serviceBlock(t, renderCompose(t, phalaData(true)), "0g-controller")

	if strings.Contains(controller, "0g-serving-provider-broker") {
		t.Errorf("the controller depends on the broker, which cannot start until the controller serves it:\n%s", controller)
	}
}

// The image the broker announces on-chain and the image compose starts must be the same one.
func TestTheReportedImageIsThePinnedImage(t *testing.T) {
	compose := renderCompose(t, phalaData(true))
	repo, digest := splitPinnedImage(brokerImage)

	if repo == "" || digest == "" {
		t.Fatalf("brokerImage %q pins no digest", brokerImage)
	}
	if !strings.Contains(compose, "image: "+repo+"@"+digest) {
		t.Errorf("no service runs the pinned reference %s@%s", repo, digest)
	}
	broker := serviceBlock(t, compose, "0g-serving-provider-broker")
	if !strings.Contains(broker, "IMAGE_REPO="+repo) || !strings.Contains(broker, "IMAGE_DIGEST="+digest) {
		t.Errorf("the broker announces something other than the image it runs:\n%s", broker)
	}
}

// A reference that pins nothing yields nothing, rather than a repository with an invented
// digest — which would write a real on-chain image change out of a missing value, and the
// contract un-acknowledges the provider's TEE signer for any such change.
func TestSplitPinnedImageRefusesToGuess(t *testing.T) {
	for _, ref := range []string{"repo:latest", "repo", ""} {
		if repo, digest := splitPinnedImage(ref); repo != "" || digest != "" {
			t.Errorf("splitPinnedImage(%q) = (%q, %q), want empty", ref, repo, digest)
		}
	}
	if repo, digest := splitPinnedImage("ghcr.io/x@sha256:abc"); repo != "ghcr.io/x" || digest != "sha256:abc" {
		t.Errorf("splitPinnedImage() = (%q, %q)", repo, digest)
	}
}
