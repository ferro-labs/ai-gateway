# NeuralBridge Reference Plugin

This example implements a complete `on_error` + `after_request` + metadata-passing plugin for Ferro AI Gateway, based on NeuralBridge's production self-healing engine.

## Architecture

The plugin implements 4-level escalating recovery (L1-L4) modeled on NeuralBridge's flywheel:

- **L1**: Retry same provider (transient errors)
- **L2**: Model downgrade within provider (rate limits)
- **L3**: Cross-provider failover (provider outages)
- **L4**: Learned flywheel rules (historical pattern matching)

After recovery, `after_request` validates the recovered response and records heal latency.

## Building

```bash
cd examples/neuralbridge-plugin
go build -buildmode=plugin -o neuralbridge.so main.go
```

## Configuration

Add to your Ferro Gateway config:

```yaml
plugins:
  - name: neuralbridge-selfheal
    path: ./examples/neuralbridge-plugin/neuralbridge.so
    stage: [on_error, after_request]
```

## NeuralBridge SDK

The full SDK (including Go version v4.0.2) is available at:
https://github.com/hhhfs9s7y9-code/neuralbridge-sdk

```
go get github.com/hhhfs9s7y9-code/neuralbridge-sdk-go/v4@v4.0.2
```
