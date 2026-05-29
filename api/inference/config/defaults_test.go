package config

import (
	"testing"
	"time"

	"github.com/0glabs/0g-serving-broker/common/config"
)

// TestDefaults_GoldenValues pins the values that used to live in the
// GetConfig() struct literal so a future tag tweak can't silently shift a
// default. Each field listed here corresponds to a default tag added when
// the literal was retired.
func TestDefaults_GoldenValues(t *testing.T) {
	cfg := &Config{}
	if err := applyDefaults(cfg); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	// Logger.Path can't be a tag (Controller.Logger needs a different value),
	// matches what loadConfig() does after the walker runs.
	if cfg.Logger != nil && cfg.Logger.Path == "" {
		cfg.Logger.Path = "./logs/inference.log"
	}
	if cfg.Controller.Logger != nil && cfg.Controller.Logger.Path == "" {
		cfg.Controller.Logger.Path = "./logs/controller.log"
	}

	checkStr := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	checkInt := func(name string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	checkDur := func(name string, got, want time.Duration) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}

	if len(cfg.AllowOrigins) != 1 || cfg.AllowOrigins[0] != "*" {
		t.Errorf("AllowOrigins = %v, want [\"*\"]", cfg.AllowOrigins)
	}
	checkStr("ContractAddress", cfg.ContractAddress, "0x47340d900bdFec2BD393c626E12ea0656F938d84")
	checkStr("Database.DSN", cfg.Database.DSN, "root:123456@tcp(mysql:3306)/provider?parseTime=true")
	checkStr("Event.ListenAddr", cfg.Event.ListenAddr, ":8088")
	checkStr("GasPrice", cfg.GasPrice, "2000000007")

	checkDur("Interval.AutoSettleBufferTime", cfg.Interval.AutoSettleBufferTime, 60*time.Second)
	checkDur("Interval.ForceSettlementProcessor", cfg.Interval.ForceSettlementProcessor, 10*time.Minute)
	checkDur("Interval.SettlementProcessor", cfg.Interval.SettlementProcessor, 5*time.Minute)
	checkDur("Interval.ReconciliationProcessor", cfg.Interval.ReconciliationProcessor, 60*time.Second)

	checkStr("Settlement.MinSettlementFee", cfg.Settlement.MinSettlementFee, "4000000000000000")
	checkStr("RevenueTransfer.ReserveAmount", cfg.RevenueTransfer.ReserveAmount, "10000000000000000000")
	checkDur("RevenueTransfer.Interval", cfg.RevenueTransfer.Interval, time.Hour)

	checkStr("Monitor.EventAddress", cfg.Monitor.EventAddress, "0g-serving-provider-event:3081")
	checkStr("ZK.URL", cfg.ZK.URL, "nginx:3001")
	checkInt("ZK.RequestLength", cfg.ZK.RequestLength, 40)

	checkStr("LoRA.LoraModulesDir", cfg.LoRA.LoraModulesDir, "/data/lora-modules")
	checkStr("LoRA.SllmUrl", cfg.LoRA.SllmUrl, "http://sllm:8343")
	checkDur("LoRA.OffloadAfter", cfg.LoRA.OffloadAfter, 60*time.Minute)
	checkDur("LoRA.PollBlockInterval", cfg.LoRA.PollBlockInterval, 5*time.Second)

	checkDur("ChatCacheExpiration", cfg.ChatCacheExpiration, 20*time.Minute)

	if cfg.Logger == nil {
		t.Fatal("Logger should be auto-instantiated")
	}
	checkStr("Logger.Format", string(cfg.Logger.Format), "text")
	checkStr("Logger.Level", cfg.Logger.Level, "info")
	checkStr("Logger.Path", cfg.Logger.Path, "./logs/inference.log")
	if cfg.Logger.RotationCount != 7 {
		t.Errorf("Logger.RotationCount = %d, want 7", cfg.Logger.RotationCount)
	}

	checkStr("LogPaths.BrokerLogDir", cfg.LogPaths.BrokerLogDir, "/var/log/inference")
	checkStr("LogPaths.EventLogDir", cfg.LogPaths.EventLogDir, "/var/log/event")

	checkInt("Controller.Port", cfg.Controller.Port, 3090)
	checkStr("Controller.Image", cfg.Controller.Image, "ghcr.io/0gfoundation/0g-serving-broker:latest")
	checkStr("Controller.Docker.Host", cfg.Controller.Docker.Host, "unix:///var/run/docker.sock")
	checkStr("Controller.Docker.APIVersion", cfg.Controller.Docker.APIVersion, "1.41")
	checkStr("Controller.Containers.Broker", cfg.Controller.Containers.Broker, "0g-serving-provider-broker")
	checkStr("Controller.Containers.Event", cfg.Controller.Containers.Event, "0g-serving-provider-event")
	checkStr("Controller.Containers.Ingress", cfg.Controller.Containers.Ingress, "broker-ingress")
	checkStr("Controller.Containers.PrometheusInit", cfg.Controller.Containers.PrometheusInit, "prometheus-init")
	checkStr("Controller.Containers.Prometheus", cfg.Controller.Containers.Prometheus, "prometheus")
	if cfg.Controller.Logger == nil {
		t.Fatal("Controller.Logger should be auto-instantiated")
	}
	checkStr("Controller.Logger.Format", string(cfg.Controller.Logger.Format), "text")
	checkStr("Controller.Logger.Level", cfg.Controller.Logger.Level, "info")
	checkStr("Controller.Logger.Path", cfg.Controller.Logger.Path, "./logs/controller.log")

	if cfg.CacheTokenBilling.Divisor != 4 {
		t.Errorf("CacheTokenBilling.Divisor = %d, want 4", cfg.CacheTokenBilling.Divisor)
	}

	checkInt("ConcurrencyLimit.MaxGlobalConcurrent", cfg.ConcurrencyLimit.MaxGlobalConcurrent, 20)
	checkInt("ConcurrencyLimit.MaxPerUserConcurrent", cfg.ConcurrencyLimit.MaxPerUserConcurrent, 5)
	checkInt("ConcurrencyLimit.PerUserRPM", cfg.ConcurrencyLimit.PerUserRPM, 30)
	checkInt("ConcurrencyLimit.PerUserBurst", cfg.ConcurrencyLimit.PerUserBurst, 5)

	if !cfg.Async.Enabled {
		t.Error("Async.Enabled = false, want true")
	}
	checkInt("Async.MaxConcurrentJobs", cfg.Async.MaxConcurrentJobs, 10)
	checkInt("Async.MaxQueueSize", cfg.Async.MaxQueueSize, 100)
	checkDur("Async.ResultTTL", cfg.Async.ResultTTL, 30*time.Minute)
	checkDur("Async.CleanupInterval", cfg.Async.CleanupInterval, 60*time.Second)
	checkDur("Async.JobTimeout", cfg.Async.JobTimeout, 15*time.Minute)

	checkDur("ProviderHttp.TotalTimeout", cfg.ProviderHttp.TotalTimeout, 15*time.Minute)
	checkDur("ProviderHttp.ResponseHeaderTimeout", cfg.ProviderHttp.ResponseHeaderTimeout, 15*time.Minute)
}

// TestDefaults_LoggerType ensures the LogFormat named-string type at
// common/config/LoggerConfig.Format accepts the "text" default through the
// reflection walker.
func TestDefaults_LoggerType(t *testing.T) {
	var lc config.LoggerConfig
	if err := applyDefaults(&lc); err != nil {
		t.Fatalf("applyDefaults LoggerConfig: %v", err)
	}
	if string(lc.Format) != "text" {
		t.Errorf("LogFormat default = %q, want \"text\"", lc.Format)
	}
}
