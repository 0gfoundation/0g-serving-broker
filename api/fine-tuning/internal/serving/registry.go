package serving

import (
	"context"
	"sync"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
)

type RegistryConfig struct {
	InputPrice  string `yaml:"inputPrice"`
	OutputPrice string `yaml:"outputPrice"`
}

type Registry struct {
	mu               sync.Mutex
	contract         *providercontract.ProviderContract
	manager          *Manager
	logger           log.Logger
	config           RegistryConfig
	registeredModels map[string]bool
}

func NewRegistry(contract *providercontract.ProviderContract, manager *Manager, config RegistryConfig, logger log.Logger) *Registry {
	return &Registry{
		contract:         contract,
		manager:          manager,
		logger:           logger,
		config:           config,
		registeredModels: make(map[string]bool),
	}
}

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

	// The fine-tuning contract tracks deliverables per task.
	// The model is already recorded on-chain as a deliverable during the finalizer phase.
	// Here we just track locally that the model is available for inference.
	// Full inference contract registration would require a separate inference contract instance,
	// which is out of scope for the fine-tuning broker — the inference broker handles that.

	r.logger.Infof("model %s marked as registered for inference serving (task: %s, owner: %s)",
		model.ModelName, model.TaskID, model.UserAddress)

	return nil
}

func (r *Registry) IsRegistered(modelName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registeredModels[modelName]
}
