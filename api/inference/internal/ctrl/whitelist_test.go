package ctrl

import (
	"strings"
	"testing"

	logrus "github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// TestIsValidEthereumAddress tests the Ethereum address validation function
func TestIsValidEthereumAddress(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		expected bool
	}{
		{
			name:     "valid lowercase address",
			address:  "0xabcdef1234567890abcdef1234567890abcdef12",
			expected: true,
		},
		{
			name:     "valid uppercase address",
			address:  "0xABCDEF1234567890ABCDEF1234567890ABCDEF12",
			expected: true,
		},
		{
			name:     "valid mixed case address",
			address:  "0xAbCdEf1234567890AbCdEf1234567890AbCdEf12",
			expected: true,
		},
		{
			name:     "invalid - too short",
			address:  "0xabcdef123456",
			expected: false,
		},
		{
			name:     "invalid - too long",
			address:  "0xabcdef1234567890abcdef1234567890abcdef1234",
			expected: false,
		},
		{
			name:     "invalid - missing 0x prefix",
			address:  "abcdef1234567890abcdef1234567890abcdef12",
			expected: false,
		},
		{
			name:     "invalid - contains non-hex characters",
			address:  "0xghijkl1234567890abcdef1234567890abcdef12",
			expected: false,
		},
		{
			name:     "invalid - empty string",
			address:  "",
			expected: false,
		},
		{
			name:     "invalid - only 0x prefix",
			address:  "0x",
			expected: false,
		},
		{
			name:     "valid - all zeros",
			address:  "0x0000000000000000000000000000000000000000",
			expected: true,
		},
		{
			name:     "valid - all f's",
			address:  "0xffffffffffffffffffffffffffffffffffffffff",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidEthereumAddress(tt.address)
			if result != tt.expected {
				t.Errorf("isValidEthereumAddress(%q) = %v, want %v", tt.address, result, tt.expected)
			}
		})
	}
}

// TestIsWhitelistedUser tests the whitelist user checking functionality
func TestIsWhitelistedUser(t *testing.T) {
	tests := []struct {
		name           string
		whitelistAddrs []string
		testAddress    string
		expected       bool
	}{
		{
			name:           "exact match lowercase",
			whitelistAddrs: []string{"0xabcdef1234567890abcdef1234567890abcdef12"},
			testAddress:    "0xabcdef1234567890abcdef1234567890abcdef12",
			expected:       true,
		},
		{
			name:           "case insensitive match - whitelist uppercase, test lowercase",
			whitelistAddrs: []string{"0xABCDEF1234567890ABCDEF1234567890ABCDEF12"},
			testAddress:    "0xabcdef1234567890abcdef1234567890abcdef12",
			expected:       true,
		},
		{
			name:           "case insensitive match - whitelist lowercase, test uppercase",
			whitelistAddrs: []string{"0xabcdef1234567890abcdef1234567890abcdef12"},
			testAddress:    "0xABCDEF1234567890ABCDEF1234567890ABCDEF12",
			expected:       true,
		},
		{
			name:           "case insensitive match - mixed case",
			whitelistAddrs: []string{"0xAbCdEf1234567890AbCdEf1234567890AbCdEf12"},
			testAddress:    "0xaBcDeF1234567890aBcDeF1234567890aBcDeF12",
			expected:       true,
		},
		{
			name:           "not in whitelist",
			whitelistAddrs: []string{"0xabcdef1234567890abcdef1234567890abcdef12"},
			testAddress:    "0x1111111111111111111111111111111111111111",
			expected:       false,
		},
		{
			name:           "empty whitelist",
			whitelistAddrs: []string{},
			testAddress:    "0xabcdef1234567890abcdef1234567890abcdef12",
			expected:       false,
		},
		{
			name:           "multiple addresses in whitelist",
			whitelistAddrs: []string{
				"0x1111111111111111111111111111111111111111",
				"0x2222222222222222222222222222222222222222",
				"0xabcdef1234567890abcdef1234567890abcdef12",
			},
			testAddress: "0xabcdef1234567890abcdef1234567890abcdef12",
			expected:    true,
		},
		{
			name:           "address not in multiple whitelist",
			whitelistAddrs: []string{
				"0x1111111111111111111111111111111111111111",
				"0x2222222222222222222222222222222222222222",
			},
			testAddress: "0xabcdef1234567890abcdef1234567890abcdef12",
			expected:    false,
		},
		{
			name:           "invalid address format in whitelist gets skipped",
			whitelistAddrs: []string{
				"invalid-address",
				"0xabcdef1234567890abcdef1234567890abcdef12",
			},
			testAddress: "0xabcdef1234567890abcdef1234567890abcdef12",
			expected:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a minimal config for testing
			cfg := &config.Config{
				Whitelist: config.WhitelistConfig{
					Enabled:       true,
					UserAddresses: tt.whitelistAddrs,
				},
			}

			// Create a minimal logger for testing
			testLogger := &testLoggerImpl{}

			// Create Ctrl instance with minimal dependencies
			// Note: We only need to test whitelist functionality, so nil for other deps is OK
			ctrl := &Ctrl{
				whitelistUsers: make(map[string]struct{}),
				logger:         testLogger,
			}

			// Initialize whitelist (mimicking the logic in New())
			if cfg.Whitelist.Enabled {
				for _, addr := range cfg.Whitelist.UserAddresses {
					if !isValidEthereumAddress(addr) {
						continue
					}
					normalizedAddr := strings.ToLower(addr)
					ctrl.whitelistUsers[normalizedAddr] = struct{}{}
				}
			}

			// Test IsWhitelistedUser
			result := ctrl.IsWhitelistedUser(tt.testAddress)
			if result != tt.expected {
				t.Errorf("IsWhitelistedUser(%q) = %v, want %v", tt.testAddress, result, tt.expected)
			}
		})
	}
}

// TestWhitelistDisabled tests that whitelist is disabled when config says so
func TestWhitelistDisabled(t *testing.T) {
	cfg := &config.Config{
		Whitelist: config.WhitelistConfig{
			Enabled: false,
			UserAddresses: []string{
				"0xabcdef1234567890abcdef1234567890abcdef12",
			},
		},
	}

	testLogger := &testLoggerImpl{}

	ctrl := &Ctrl{
		whitelistUsers: make(map[string]struct{}),
		logger:         testLogger,
	}

	// When disabled, whitelist should be empty
	if cfg.Whitelist.Enabled {
		for _, addr := range cfg.Whitelist.UserAddresses {
			if !isValidEthereumAddress(addr) {
				continue
			}
			normalizedAddr := strings.ToLower(addr)
			ctrl.whitelistUsers[normalizedAddr] = struct{}{}
		}
	}

	// Even though address is in config, it should not be whitelisted
	result := ctrl.IsWhitelistedUser("0xabcdef1234567890abcdef1234567890abcdef12")
	if result != false {
		t.Errorf("Expected whitelist to be disabled, but user was whitelisted")
	}
}

// testLoggerImpl is a minimal logger implementation for testing
type testLoggerImpl struct{}

func (l *testLoggerImpl) Debug(args ...interface{})                      {}
func (l *testLoggerImpl) Info(args ...interface{})                       {}
func (l *testLoggerImpl) Print(args ...interface{})                      {}
func (l *testLoggerImpl) Warn(args ...interface{})                       {}
func (l *testLoggerImpl) Warning(args ...interface{})                    {}
func (l *testLoggerImpl) Error(args ...interface{})                      {}
func (l *testLoggerImpl) Fatal(args ...interface{})                      {}
func (l *testLoggerImpl) Panic(args ...interface{})                      {}
func (l *testLoggerImpl) Debugf(format string, args ...interface{})      {}
func (l *testLoggerImpl) Infof(format string, args ...interface{})       {}
func (l *testLoggerImpl) Printf(format string, args ...interface{})      {}
func (l *testLoggerImpl) Warnf(format string, args ...interface{})       {}
func (l *testLoggerImpl) Warningf(format string, args ...interface{})    {}
func (l *testLoggerImpl) Errorf(format string, args ...interface{})      {}
func (l *testLoggerImpl) Fatalf(format string, args ...interface{})      {}
func (l *testLoggerImpl) Panicf(format string, args ...interface{})      {}
func (l *testLoggerImpl) Debugln(args ...interface{})                    {}
func (l *testLoggerImpl) Infoln(args ...interface{})                     {}
func (l *testLoggerImpl) Println(args ...interface{})                    {}
func (l *testLoggerImpl) Warnln(args ...interface{})                     {}
func (l *testLoggerImpl) Warningln(args ...interface{})                  {}
func (l *testLoggerImpl) Errorln(args ...interface{})                    {}
func (l *testLoggerImpl) Fatalln(args ...interface{})                    {}
func (l *testLoggerImpl) Panicln(args ...interface{})                    {}
func (l *testLoggerImpl) WithFields(fields logrus.Fields) log.Logger     { return l }
func (l *testLoggerImpl) InnerLogger() *logrus.Logger                    { return nil }
