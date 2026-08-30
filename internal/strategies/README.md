# Routing strategies

A **strategy** decides the *order* in which a request's candidate targets are
tried — and only that. Retry, the circuit breaker, per-target concurrency, error
classification, metrics and request logging live in the gateway's request
pipeline, one layer up, so they behave identically under every strategy. (This is
the split Envoy makes too: the route table and load balancer say *where* a
request may go, while the retry policy and circuit breaker hang off the route and
the cluster.)

Pick a strategy with `strategy.mode`. Every target names a `virtual_key` (a
provider), and a request routes only to targets that serve its model. See
[`../../config.example.yaml`](../../config.example.yaml) for the full config
reference with inline comments.

```yaml
strategy:
  mode: fallback
targets:
  - virtual_key: openai
  - virtual_key: anthropic
```

## Two families

What a strategy does when its chosen target *fails* splits the eight modes in two:

| Family | Modes | On a failure |
|---|---|---|
| **Pool** | fallback, loadbalance, least-latency, cost-optimized, ab-test | the request advances only after a failover-safe failure |
| **Named** | single, conditional, content-based | the request stops and the failure is returned |

A pool mode picks its target for a reason about the *pool* (spread load, take the
cheapest, take the fastest, split traffic), so carrying a failed request to a
sibling is what was asked for. A named mode picks a *specific* target (you named
it, or a rule matched it), so serving from somewhere else would demote the rule
to a suggestion.

Failover-safe failures are transport failures, 408, 429, 5xx, open circuits,
and target saturation. Cancellation, deadline expiry, and every other 4xx stop
at the current target; named modes also stop after any provider-call failure.

Under **every** mode, a target whose **circuit breaker is open** is skipped when
the choice is made, so a backend the gateway has already decided not to call
takes no traffic while a healthy target is configured. When every candidate's
circuit is open the request is still attempted and answered `503`.

## The strategies

### single
Always the first configured target. A request for a model that target does not
serve is `model_not_found`, even if another configured target serves it — give
such a model its own target and a mode that can reach it.

### fallback
Tries targets in configured order and moves to the next after a failover-safe
failure.

```yaml
strategy: { mode: fallback }
```

### loadbalance
Distributes requests across targets by `weight`. A weight of `0` drains a target
(no traffic) without removing it; at least one weight must be positive.

```yaml
strategy: { mode: loadbalance }
targets:
  - { virtual_key: openai, weight: 3 }
  - { virtual_key: azure-openai, weight: 1 }
```

### least-latency
Routes to the target with the lowest observed p50 latency, so traffic follows
the currently-fastest backend.

```yaml
strategy: { mode: least-latency }
```

### cost-optimized
Routes to the cheapest catalog-priced target. `unpriced_strategy` decides what
happens for a target the catalog has no price for:

| `unpriced_strategy` | Behaviour |
|---|---|
| `fallback` (default) | prefer priced targets, then the first compatible unpriced one |
| `skip` | reject unpriced candidates |
| `allow` | treat a missing price as zero cost |

```yaml
strategy:
  mode: cost-optimized
  unpriced_strategy: fallback
```

### conditional
Routes by the value of a request field. Rules are evaluated in order, first match
wins; when no rule matches, the request goes to the fallback target. Supported
keys: `model`, `model_prefix`.

```yaml
strategy:
  mode: conditional
  conditions:
    - { key: model, value: gpt-4o, target_key: openai }
    - { key: model_prefix, value: claude, target_key: anthropic }
```

### content-based
Routes by the textual content of the prompt. Rules evaluate in order, first match
wins; no match falls back to the first target. Supported types: `prompt_contains`,
`prompt_not_contains`, `prompt_regex`.

```yaml
strategy:
  mode: content-based
  content_conditions:
    - { type: prompt_regex, value: "(?i)\\b(code|function)\\b", target_key: deepseek }
    - { type: prompt_contains, value: "translate", target_key: gemini }
```

### ab-test
Splits traffic across labelled variants by weight (relative shares). A `0`-weight
variant receives nothing — how you drain one before removing it; at least one
weight must be positive.

```yaml
strategy:
  mode: ab-test
  ab_variants:
    - { target_key: openai, weight: 70, label: control }
    - { target_key: anthropic, weight: 30, label: challenger }
```

## `/v1/models` stays in step

The model listing reflects what the *active* mode would actually route: a model
absent from `/v1/models` is the signal that routing would refuse it. So under a
named mode, list only the models the configured rules/target can reach.

## Quick check

`scripts/strategy_smoke.sh` exercises all eight strategies (and their edge cases)
end-to-end against live providers and prints a PASS/FAIL summary.
