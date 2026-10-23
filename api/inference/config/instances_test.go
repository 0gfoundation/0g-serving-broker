package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two distinct dev keys; only their derived addresses matter here.
const (
	testKeyA = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	testKeyB = "0x5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
)

func instanceYAML(servingURL, key string, extra string) string {
	return "service:\n" +
		"  servingUrl: " + servingURL + "\n" +
		"  targetUrl: http://engine:8000/v1\n" +
		"  type: chatbot\n" +
		"  model: m\n" +
		"  inputPrice: \"1\"\n" +
		"  outputPrice: \"1\"\n" +
		"network:\n" +
		"  url: http://rpc\n" +
		"  chainID: 16661\n" +
		"  privateKeys: [\"" + key + "\"]\n" + extra
}

func writeInstanceDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func primaryForInstances(t *testing.T) *Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(instanceYAML("https://a.example", testKeyA, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", p)
	cfg := defaultConfig()
	if err := loadConfig(cfg); err != nil {
		t.Fatalf("loading the primary: %v", err)
	}
	return cfg
}

func TestLoadInstancesUnsetIsNone(t *testing.T) {
	t.Setenv(InstancesDirEnvVar, "")
	got, err := LoadInstances(&Config{})
	if err != nil || got != nil {
		t.Fatalf("LoadInstances = %v, %v; want nil, nil", got, err)
	}
}

// An extra instance's database server is the attested one: host and
// credentials from DATABASE_DSN, only the database name its own. TARGET_URL is
// the primary's upstream and must not reach it.
func TestLoadInstancesTakesAttestedDatabaseServerNotPrimaryUpstream(t *testing.T) {
	t.Setenv(databaseDSNEnvVar, "root:pw@tcp(mysql:3306)/provider?parseTime=true")
	t.Setenv(targetURLEnvVar, "http://primary-engine:8000/v1")
	primary := primaryForInstances(t)

	t.Setenv(InstancesDirEnvVar, writeInstanceDir(t, map[string]string{
		"image-gen.yaml": instanceYAML("https://b.example", testKeyB, ""),
	}))
	got, err := LoadInstances(primary)
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(got) != 1 || got[0].Name != "image-gen" {
		t.Fatalf("instances = %+v, want one named image-gen", got)
	}
	c := got[0].Config
	if want := "root:pw@tcp(mysql:3306)/provider_image_gen?parseTime=true"; c.Database.DSN != want {
		t.Errorf("dsn = %q, want %q", c.Database.DSN, want)
	}
	if c.Service.TargetURL != "http://engine:8000/v1" {
		t.Errorf("targetUrl = %q, want the instance's own", c.Service.TargetURL)
	}
	if primary.Service.TargetURL != "http://primary-engine:8000/v1" {
		t.Errorf("primary targetUrl = %q, want TARGET_URL", primary.Service.TargetURL)
	}
}

// A database the instance's config names keeps its name but not its server.
func TestLoadInstancesKeepsConfiguredDatabaseName(t *testing.T) {
	t.Setenv(databaseDSNEnvVar, "root:pw@tcp(mysql:3306)/provider?parseTime=true")
	primary := primaryForInstances(t)
	t.Setenv(InstancesDirEnvVar, writeInstanceDir(t, map[string]string{
		"b.yaml": instanceYAML("https://b.example", testKeyB, "database:\n  dsn: other:x@tcp(elsewhere:3306)/legacy_b\n"),
	}))
	got, err := LoadInstances(primary)
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if want := "root:pw@tcp(mysql:3306)/legacy_b?parseTime=true"; got[0].Config.Database.DSN != want {
		t.Errorf("dsn = %q, want %q", got[0].Config.Database.DSN, want)
	}
}

func TestLoadInstancesRefusesWhatOneProcessCannotServe(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"same host as the primary", map[string]string{"b.yaml": instanceYAML("https://A.example:443", testKeyB, "")}, "both serve host"},
		{"same wallet as the primary", map[string]string{"b.yaml": instanceYAML("https://b.example", testKeyA, "")}, "same wallet"},
		{"servingUrl with a path", map[string]string{"b.yaml": instanceYAML("https://b.example/b", testKeyB, "")}, "has a path"},
		{"another chain", map[string]string{"b.yaml": strings.Replace(instanceYAML("https://b.example", testKeyB, ""), "16661", "16602", 1)}, "network.chainID"},
		{"a name the controller cannot address", map[string]string{"B_1.yaml": instanceYAML("https://b.example", testKeyB, "")}, "file name"},
		{"the primary's name", map[string]string{"primary.yaml": instanceYAML("https://b.example", testKeyB, "")}, "CONFIG_FILE instance"},
		{"an invalid config", map[string]string{"b.yaml": "service:\n  type: nonsense\n"}, `instance "b"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(databaseDSNEnvVar, "root:pw@tcp(mysql:3306)/provider")
			primary := primaryForInstances(t)
			t.Setenv(InstancesDirEnvVar, writeInstanceDir(t, tc.files))
			_, err := LoadInstances(primary)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadInstances error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestServingHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://A.Example":       "a.example",
		"https://a.example.":      "a.example",
		"https://a.example/":      "a.example",
		"http://a.example:3080":   "a.example",
		" https://a.example:443 ": "a.example",
	} {
		if got, err := ServingHost(in); err != nil || got != want {
			t.Errorf("ServingHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "a.example", "ftp://a.example", "https://a.example/v1"} {
		if _, err := ServingHost(in); err == nil {
			t.Errorf("ServingHost(%q) accepted", in)
		}
	}
}

// Extra instances need DATABASE_DSN, even when their file names a database:
// otherwise the broker and event containers, each with its own environment,
// could open different databases for one instance.
func TestLoadInstancesRequireTheAttestedDatabase(t *testing.T) {
	t.Setenv(databaseDSNEnvVar, "")
	primary := primaryForInstances(t)
	for name, extra := range map[string]string{
		"naming no database": "",
		"naming its own":     "database:\n  dsn: root:pw@tcp(mysql:3306)/provider_b\n",
	} {
		t.Setenv(InstancesDirEnvVar, writeInstanceDir(t, map[string]string{
			"b.yaml": instanceYAML("https://b.example", testKeyB, extra),
		}))
		if _, err := LoadInstances(primary); err == nil || !strings.Contains(err.Error(), "extra instances need DATABASE_DSN") {
			t.Errorf("%s: LoadInstances error = %v, want DATABASE_DSN required", name, err)
		}
	}
}

// Two spellings of one database are one database: the primary keeps
// DATABASE_DSN's text while an instance's DSN is rebuilt by the driver, which
// writes the default port and orders parameters its own way.
func TestLoadInstancesRefusesOneDatabaseSpelledTwoWays(t *testing.T) {
	t.Setenv(databaseDSNEnvVar, "root:pw@tcp(mysql)/provider?parseTime=true")
	primary := primaryForInstances(t)
	t.Setenv(InstancesDirEnvVar, writeInstanceDir(t, map[string]string{
		"b.yaml": instanceYAML("https://b.example", testKeyB, "database:\n  dsn: root:pw@tcp(mysql:3306)/provider\n"),
	}))
	if _, err := LoadInstances(primary); err == nil || !strings.Contains(err.Error(), "same database") {
		t.Fatalf("LoadInstances error = %v, want the shared database refused", err)
	}
}

func TestLoadInstancesRefusesTwoLoRAInstances(t *testing.T) {
	t.Setenv(databaseDSNEnvVar, "root:pw@tcp(mysql:3306)/provider")
	primary := primaryForInstances(t)
	primary.LoRA.Enable = true
	t.Setenv(InstancesDirEnvVar, writeInstanceDir(t, map[string]string{
		"b.yaml": instanceYAML("https://b.example", testKeyB, "lora:\n  enable: true\n"),
	}))
	if _, err := LoadInstances(primary); err == nil || !strings.Contains(err.Error(), "both enable lora") {
		t.Fatalf("LoadInstances error = %v, want two LoRA instances refused", err)
	}
}

// The controller validates with the DATABASE_DSN the broker runs with, so it
// refuses what the broker would refuse at start, whatever its own environment.
func TestValidateInstanceSetUsesTheBrokersDatabase(t *testing.T) {
	t.Setenv(databaseDSNEnvVar, "") // the controller's own: unset
	dir := writeInstanceDir(t, map[string]string{"b.yaml": instanceYAML("https://b.example", testKeyB, "")})
	primaryContent := []byte(instanceYAML("https://a.example", testKeyA, ""))
	brokerDSN := "root:pw@tcp(mysql:3306)/provider?parseTime=true"

	if err := ValidateInstanceSet(dir, primaryContent, "", nil, brokerDSN); err != nil {
		t.Fatalf("a set the broker serves = %v", err)
	}
	onPrimary := []byte(instanceYAML("https://b.example", testKeyB, "database:\n  dsn: x:y@tcp(z)/provider\n"))
	if err := ValidateInstanceSet(dir, primaryContent, "b", onPrimary, brokerDSN); err == nil || !strings.Contains(err.Error(), "same database") {
		t.Errorf("an instance onto the primary's database = %v, want refused", err)
	}
	// A broker without DATABASE_DSN refuses extra instances, so the controller does.
	if err := ValidateInstanceSet(dir, primaryContent, "", nil, ""); err == nil || !strings.Contains(err.Error(), "extra instances need DATABASE_DSN") {
		t.Errorf("a broker without DATABASE_DSN = %v, want refused", err)
	}
}

// A config still using the legacy networks map is served by the broker, so the
// upstream record must read its wallet from it too.
func TestInProcessUpstreamSourcesReadsTheLegacyNetworksMap(t *testing.T) {
	legacy := strings.Replace(instanceYAML("https://b.example", testKeyB, ""),
		"network:\n  url: http://rpc\n  chainID: 16661\n  privateKeys: [\""+testKeyB+"\"]\n",
		"networks:\n  ethereum0g:\n    url: http://rpc\n    chainID: 16661\n    privateKeys: [\""+testKeyB+"\"]\n", 1)
	if !strings.Contains(legacy, "networks:") {
		t.Fatal("the legacy fixture did not take the legacy shape")
	}
	dir := writeInstanceDir(t, map[string]string{"b.yaml": legacy})
	got, err := InProcessUpstreamSources(dir, []byte(instanceYAML("https://a.example", testKeyA, "")), "", nil)
	if err != nil || len(got) != 2 || got[1].Provider == "" {
		t.Fatalf("InProcessUpstreamSources = %+v, %v; want both providers", got, err)
	}
}

// Every reader of the directory refuses the file names the broker refuses, so
// the upstream record cannot name an instance the broker would not load.
func TestInstanceDirectoryReadersAgreeOnNames(t *testing.T) {
	t.Setenv(databaseDSNEnvVar, "root:pw@tcp(mysql:3306)/provider")
	primaryContent := []byte(instanceYAML("https://a.example", testKeyA, ""))
	for _, file := range []string{"Primary.yaml", "primary.yaml", "b_1.yaml"} {
		dir := writeInstanceDir(t, map[string]string{file: instanceYAML("https://b.example", testKeyB, "")})
		t.Setenv(InstancesDirEnvVar, dir)
		if _, err := LoadInstances(primaryForInstances(t)); err == nil {
			t.Errorf("%s: LoadInstances accepted it", file)
		}
		if err := ValidateInstanceSet(dir, primaryContent, "", nil, "root:pw@tcp(mysql:3306)/provider"); err == nil {
			t.Errorf("%s: ValidateInstanceSet accepted it", file)
		}
		if _, err := InProcessUpstreamSources(dir, primaryContent, "", nil); err == nil {
			t.Errorf("%s: InProcessUpstreamSources recorded it", file)
		}
	}
}
