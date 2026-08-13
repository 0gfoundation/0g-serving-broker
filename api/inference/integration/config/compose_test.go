package main

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
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
	return dataFor("phala", useController)
}

func dataFor(node TeeNode, useController bool) TemplateData {
	repo, digest := splitPinnedImage(brokerImage)
	return TemplateData{
		BrokerImage:      brokerImage,
		TeeNode:          node,
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

// Every reachable combination must render to YAML docker compose can parse.
//
// This is the check that was missing, and the gap a real defect went through: deleting one
// service's depends_on header left another's conditional entry parentless, which only shows up
// with UseNginx on — a flag no other test here sets. The fixtures below vary every flag the
// template branches on, and the assertion is a parse rather than a substring, because a
// template can emit something that contains all the right strings and still not be a
// compose file.
func TestEveryCombinationRendersParseableYAML(t *testing.T) {
	for _, node := range []TeeNode{"phala", "hardhat", "alicloud"} {
		for _, controller := range []bool{false, true} {
			for _, nginx := range []bool{false, true} {
				for _, monitoring := range []bool{false, true} {
					for _, fileLog := range []bool{false, true} {
						data := dataFor(node, controller)
						data.UseNginx = nginx
						data.UseMonitoring = monitoring
						data.EnableFileLog = fileLog

						name := string(node)
						for flag, on := range map[string]bool{"nginx": nginx, "monitoring": monitoring, "filelog": fileLog, "controller": controller} {
							if on {
								name += "+" + flag
							}
						}
						t.Run(name, func(t *testing.T) {
							rendered := renderCompose(t, data)
							var parsed struct {
								Services map[string]any `yaml:"services"`
								Volumes  map[string]any `yaml:"volumes"`
							}
							if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
								t.Fatalf("the generated compose is not parseable YAML: %v", err)
							}
							if len(parsed.Services) == 0 {
								t.Fatal("the generated compose defines no services")
							}
							// Every volume a service mounts by name must be declared, or compose
							// refuses the file at startup rather than at parse.
							if strings.Contains(rendered, "zg-tee:"+attestSocketDir) {
								if _, ok := parsed.Volumes["zg-tee"]; !ok {
									t.Error("services mount zg-tee but it is not declared")
								}
							}
						})
					}
				}
			}
		}
	}
}

// A deployment without a controller must render exactly what it rendered before this change,
// for every combination — not merely contain the same few substrings in the broker block.
//
// Checked as a property rather than against a golden file: nothing outside the hardened
// branches may mention any of the names this change introduced.
func TestWithoutAControllerNothingNewAppears(t *testing.T) {
	introduced := []string{"TEE_SOCKET", "ATTEST_PROXY_SOCKET", "IMAGE_REPO", "IMAGE_DIGEST", "zg-tee", attestSocketDir}

	for _, node := range []TeeNode{"phala", "hardhat", "alicloud"} {
		for _, nginx := range []bool{false, true} {
			for _, monitoring := range []bool{false, true} {
				data := dataFor(node, false)
				data.UseNginx = nginx
				data.UseMonitoring = monitoring

				rendered := renderCompose(t, data)
				for _, name := range introduced {
					if strings.Contains(rendered, name) {
						t.Errorf("%s: a controller-less deployment carries %q", node, name)
					}
				}
			}
		}
	}
}

// hardhat and alicloud have no attestation proxy — the mounts and ATTEST_PROXY_SOCKET are
// gated on the TEE node — so they must not be told to use one either. A broker pointed at a
// socket nothing serves, or announcing an image pair no controller there can change, is worse
// than plain.
func TestNonPhalaNodesGetNoHalfHardenedDeployment(t *testing.T) {
	for _, node := range []TeeNode{"hardhat", "alicloud"} {
		rendered := renderCompose(t, dataFor(node, true))
		for _, name := range []string{"TEE_SOCKET", "ATTEST_PROXY_SOCKET", "IMAGE_REPO", "zg-tee"} {
			if strings.Contains(rendered, name) {
				t.Errorf("%s with a controller carries %q, but has no proxy to serve it", node, name)
			}
		}
	}
}

// Only the controller may write the config file, and that is what makes zg-config-update a
// complete account of it rather than an audit trail of one route among several.
//
// The file lives inside the CVM, so the provider's host cannot reach it — but a read-write
// mount lets the container holding it rewrite its own pricing, targetUrl or verifiability with
// no record at all, which would leave "no config record" meaning nothing. Nothing in the
// broker or the event service writes it: the only writer in the tree is ApplyCoreConfig.
func TestOnlyTheControllerCanWriteTheConfig(t *testing.T) {
	for _, node := range []TeeNode{"phala", "hardhat", "alicloud"} {
		for _, controller := range []bool{false, true} {
			compose := renderCompose(t, dataFor(node, controller))

			for _, service := range []string{"0g-serving-provider-broker", "0g-serving-provider-event"} {
				block := serviceBlock(t, compose, service)
				if !strings.Contains(block, "/etc/config.yaml:ro") {
					t.Errorf("%s (%s, controller=%v) mounts the config writable, so it could rewrite its own pricing with no record", service, node, controller)
				}
			}

			if !controller {
				continue
			}
			// The controller's own mount must stay writable — it is the writer.
			ctl := serviceBlock(t, compose, "0g-controller")
			if !strings.Contains(ctl, "/etc/config.yaml") || strings.Contains(ctl, "/etc/config.yaml:ro") {
				t.Errorf("the controller cannot write the config, so ApplyCoreConfig would fail:\n%s", ctl)
			}
		}
	}
}

// Every service the controller acts on must pin its container name.
//
// The controller finds containers by exact name and refuses anything else, because its lookup
// otherwise falls back to a shortest-substring match — which after a failed recreate resolves
// "0g-serving-provider-broker" to "0g-serving-provider-broker-db", this project's own database.
// Without container_name, compose names the container <project>-<service>-1 and EVERY upgrade
// is refused. A test rather than a comment, so adding a service the controller manages cannot
// silently reintroduce it.
func TestTheControllersContainersPinTheirNames(t *testing.T) {
	compose := renderCompose(t, phalaData(true))

	for _, service := range []string{"0g-serving-provider-broker", "0g-serving-provider-event", "0g-controller"} {
		block := serviceBlock(t, compose, service)
		if !strings.Contains(block, "container_name: "+service) {
			t.Errorf("%s does not pin container_name, so the controller would refuse to act on it:\n%s", service, block)
		}
	}

	// And NOT without one. A pinned name is global to the docker daemon, while the wizard
	// supports several deployments per host through COMPOSE_PROJECT_NAME and a per-project
	// network — so pinning unconditionally would make the second one fail on a name conflict,
	// trading an isolation property every deployment has for a guard only a controller needs.
	plain := renderCompose(t, phalaData(false))
	if strings.Contains(plain, "container_name: 0g-serving-provider-broker") {
		t.Error("a controller-less deployment pins container names, which breaks running two deployments on one host")
	}
}

// Anything a service waits on for health must actually define a healthcheck, or compose refuses
// the whole file at startup rather than at parse — which no YAML check catches.
func TestEveryHealthDependencyHasAHealthcheck(t *testing.T) {
	for _, node := range []TeeNode{"phala", "hardhat", "alicloud"} {
		for _, controller := range []bool{false, true} {
			data := dataFor(node, controller)
			data.DeployLLM = true
			data.UseNginx = true
			data.UseMonitoring = true

			var parsed struct {
				Services map[string]struct {
					Healthcheck any                       `yaml:"healthcheck"`
					DependsOn   map[string]map[string]any `yaml:"depends_on"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal([]byte(renderCompose(t, data)), &parsed); err != nil {
				t.Fatalf("%s: %v", node, err)
			}
			for name, svc := range parsed.Services {
				for dep, cond := range svc.DependsOn {
					if cond["condition"] != "service_healthy" {
						continue
					}
					target, ok := parsed.Services[dep]
					if !ok {
						t.Errorf("%s: %s waits on %s, which this render does not define", node, name, dep)
						continue
					}
					if target.Healthcheck == nil {
						t.Errorf("%s: %s waits for %s to be healthy, but %s defines no healthcheck — compose refuses to start", node, name, dep, dep)
					}
				}
			}
		}
	}
}
