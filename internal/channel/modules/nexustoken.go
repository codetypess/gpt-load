package modules

import (
	"gpt-load/internal/channel/spec"
	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
)

func NexusToken() spec.Module {
	return spec.Module{
		Definition: spec.Definition{
			ResponsesWebsocket: execution.WebsocketCapabilities{Native: true, Continuation: true, Prewarm: true, StoredResponses: false, Multiplex: false},

			ID:          spec.NexusToken,
			Name:        "NexusToken",
			Mark:        "NX",
			Icon:        "nexustoken",
			SearchTerms: []string{"nexus token", "carpool", "拼车"},
			Description: "NexusToken carpool gateway",
			Connection: spec.Connection{
				Type:            spec.ConnectionAPIKey,
				CredentialInput: "batch_text",
			},
			Params: []spec.Field{{
				Key: "base_url", Label: "Base URL", InputKind: spec.InputURL,
				Required: true, Normalizer: spec.NormalizeBaseURL, Default: "https://intl.freehyy.com",
			}},
			Credentials: []spec.Field{{
				Key: "api_key", Label: "API Key", InputKind: spec.InputSecret,
				Required: true, Sensitive: true, Normalizer: spec.NormalizeNonEmpty,
			},
				{Key: "session_id", Label: "Session", InputKind: spec.InputText, Normalizer: spec.NormalizeNonEmpty},
				{Key: "seat_id", Label: "Seat", InputKind: spec.InputText, Normalizer: spec.NormalizeNonEmpty},
				{Key: "key_id", Label: "Key ID", InputKind: spec.InputText, Normalizer: spec.NormalizeNonEmpty},
				{Key: "email", Label: "Email", InputKind: spec.InputText, Normalizer: spec.NormalizeNonEmpty},
				{Key: "plan_name", Label: "Plan", InputKind: spec.InputText, Normalizer: spec.NormalizeNonEmpty},
			},
			Capabilities: spec.CapabilityBindings{APIKeyQuotaObservation: true},
			Provider: spec.ProviderBinding{
				ProviderKind:    spec.ProviderMultiProtocolGateway,
				DefaultBaseURLs: []string{"https://intl.freehyy.com"},
				EndpointPolicy:  spec.EndpointRequiredBaseURL,
			},
			Routes: []spec.Route{
				spec.NewRoute(protocol.OpenAICompletions, execution.OperationChatCompletion, execution.RouteNative),
				spec.NewRoute(protocol.OpenAICompletions, execution.OperationListModels, execution.RouteNative),
				spec.NewRoute(protocol.OpenAICompletions, execution.OperationProbe, execution.RouteNative),
				spec.NewResponsesCreateRoute(execution.RouteNative, spec.ResponsesStoreHandlingUpstreamManaged),
				spec.NewRoute(protocol.OpenAIResponses, execution.OperationProbe, execution.RouteNative),
				spec.NewRoute(protocol.OpenAIResponses, execution.OperationResponsesCompact, execution.RouteNative),
				spec.NewRoute(protocol.OpenAIImages, execution.OperationImagesGenerate, execution.RouteNative),
				spec.NewRoute(protocol.OpenAIImages, execution.OperationImagesEdit, execution.RouteNative),
				spec.NewRoute(protocol.Anthropic, execution.OperationChatCompletion, execution.RouteNative),
				spec.NewRoute(protocol.Anthropic, execution.OperationProbe, execution.RouteNative),
				spec.NewRoute(protocol.Anthropic, execution.OperationCountTokens, execution.RouteNative),
				spec.NewRoute(protocol.Gemini, execution.OperationChatCompletion, execution.RouteNative),
				spec.NewRoute(protocol.Gemini, execution.OperationProbe, execution.RouteNative),
				spec.NewRoute(protocol.Gemini, execution.OperationCountTokens, execution.RouteNative),
			},
		},
	}
}
