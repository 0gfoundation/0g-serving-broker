package serving

import (
	"context"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
)

// RegistryConfig holds pricing configuration for registering inference services.
type RegistryConfig struct {
	InputPrice  string `yaml:"inputPrice"`
	OutputPrice string `yaml:"outputPrice"`
}

// Registry periodically synchronises the set of served LoRA models with the
// inference contract. Currently contract registration is a no-op (see registerOnContract);
// this component tracks serving state locally until the fine-tuning and inference
// brokers share a unified contract interface.
type Registry struct {
	mu               sync.Mutex
	contract         *providercontract.ProviderContract
	manager          *Manager
	logger           log.Logger
	config           RegistryConfig
	registeredModels map[string]bool
}

// NewRegistry creates a Registry that will track served models and (eventually)
// register them on the inference contract.
func NewRegistry(contract *providercontract.ProviderContract, manager *Manager, config RegistryConfig, logger log.Logger) *Registry {
	return &Registry{
		contract:         contract,
		manager:          manager,
		logger:           logger,
		config:           config,
		registeredModels: make(map[string]bool),
	}
}

// Start begins the background sync loop that tracks model registrations.
func (r *Registry) Start(ctx context.Context) {
	go r.syncLoop(ctx)
}

func (r *Registry) syncLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.syncRegistrations(ctx)
		}
	}
}

func (r *Registry) syncRegistrations(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	models := r.manager.ListServedModels()

	for _, model := range models {
		if r.registeredModels[model.ModelName] {
			continue
		}

		if err := r.registerOnContract(ctx, model); err != nil {
			r.logger.Errorf("failed to register model %s on contract: %v", model.ModelName, err)
			continue
		}

		r.registeredModels[model.ModelName] = true
		r.logger.Infof("registered model %s on inference contract", model.ModelName)
	}

	currentModels := make(map[string]bool)
	for _, m := range models {
		currentModels[m.ModelName] = true
	}
	for name := range r.registeredModels {
		if !currentModels[name] {
			delete(r.registeredModels, name)
			r.logger.Infof("removed contract registration tracking for model: %s", name)
		}
	}
}

func (r *Registry) registerOnContract(ctx context.Context, model *ServedModel) error {
	r.logger.Infof("registering fine-tuned model on contract: name=%s, task=%s, owner=%s, inputPrice=%s, outputPrice=%s",
		model.ModelName, model.TaskID, model.UserAddress, r.config.InputPrice, r.config.OutputPrice)

	// TODO: When fine-tuning and inference brokers share a contract interface, this method
	// should call contract.AddOrUpdateService() to register the LoRA model as an inference
	// service endpoint. For now the fine-tuning contract only tracks deliverables per task
	// (recorded during the finalizer phase), so we track serving state locally.
	// The inference broker is responsible for on-chain inference service registration.

	r.logger.Infof("model %s marked as registered for inference serving (task: %s, owner: %s)",
		model.ModelName, model.TaskID, model.UserAddress)

	return nil
}

// IsRegistered reports whether the given model name has been tracked as registered.
func (r *Registry) IsRegistered(modelName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registeredModels[modelName]
}
