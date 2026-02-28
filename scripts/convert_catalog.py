#!/usr/bin/env python3
"""
convert_catalog.py

Reads LiteLLM's model_prices_and_context_window.json and converts ALL models
to models/catalog.json using the Ferro catalog schema.

No provider filtering — every model in the LiteLLM file is included so the
catalog is a complete reference for cost calculation.

Usage:
    python3 scripts/convert_catalog.py

Input:  model_prices_and_context_window.json  (repo root)
Output: models/catalog.json + models/catalog_backup.json
"""

import json
import os
import shutil
import sys

# Map LiteLLM mode strings → our ModelMode constants.
MODE_MAP = {
    "chat":                  "chat",
    "completion":            "chat",
    "embedding":             "embedding",
    "image_generation":      "image",
    "audio_transcription":   "audio_in",
    "audio_speech":          "audio_out",
}

# These LiteLLM modes have no equivalent in the gateway — skip them.
SKIP_MODES = {"moderation", "rerank", "search"}

# LiteLLM provider name → canonical provider name used as catalog key prefix.
# For providers not in this map the litellm_provider value is used as-is.
PROVIDER_ALIASES = {
    "text-completion-openai":            "openai",
    "chatgpt":                           "openai",
    "openai_like":                       "openai",
    "openai_compatible":                 "openai",
    "cohere_chat":                       "cohere",
    "codestral":                         "mistral",
    "text-completion-codestral":         "mistral",
    "mistral_azure":                     "azure",
    "together_ai":                       "together",
    "fireworks_ai":                      "fireworks",
    "fireworks_ai-embedding-models":     "fireworks",
    "bedrock_converse":                  "bedrock",
    "bedrock_converse_passthrough":      "bedrock",
    "sagemaker_chat":                    "sagemaker",
    "sagemaker_meta":                    "sagemaker",
    "vertex_ai_beta":                    "vertex_ai",
    "vertex_ai-anthropic_models":        "vertex_ai",
    "vertex_ai-llama_models":            "vertex_ai",
    "vertex_ai-mistral_models":          "vertex_ai",
    "vertex_ai-image-models":            "vertex_ai",
    "vertex_ai-embedding-models":        "vertex_ai",
    "vertex_ai-vision-models":           "vertex_ai",
    "vertex_ai-code-text-models":        "vertex_ai",
    "vertex_ai-language-models":         "vertex_ai",
    "vertex_ai-text-models":             "vertex_ai",
    "vertex_ai-chat-models":             "vertex_ai",
    "vertex_ai-code-chat-models":        "vertex_ai",
    "azure_ai":                          "azure",
    "azure_text":                        "azure",
    "azure_openai":                      "azure",
    "eu.anthropic":                      "anthropic",
    "us.anthropic":                      "anthropic",
    "us.amazon":                         "bedrock",
    "eu.amazon":                         "bedrock",
    "ap.amazon":                         "bedrock",
    "us.meta":                           "bedrock",
    "eu.meta":                           "bedrock",
    "ollama_chat":                       "ollama",
    "ai21_chat":                         "ai21",
}


def canonical_provider(litellm_provider: str) -> str:
    return PROVIDER_ALIASES.get(litellm_provider, litellm_provider)


def derive_model_id(litellm_key: str, litellm_provider: str) -> str:
    canon = canonical_provider(litellm_provider)
    for prefix in (litellm_provider + "/", canon + "/"):
        if litellm_key.startswith(prefix):
            return litellm_key[len(prefix):]
    return litellm_key


def per_m(cost_per_token):
    if cost_per_token is None:
        return None
    return round(cost_per_token * 1_000_000, 10)


def convert_entry(litellm_key: str, v: dict, provider: str) -> dict | None:
    raw_mode = v.get("mode")
    if raw_mode in SKIP_MODES:
        return None
    mode = MODE_MAP.get(raw_mode, "chat")
    model_id = derive_model_id(litellm_key, v.get("litellm_provider", ""))

    if mode == "embedding":
        pricing = {
            "input_per_m_tokens": None, "output_per_m_tokens": None,
            "cache_read_per_m_tokens": None, "cache_write_per_m_tokens": None,
            "reasoning_per_m_tokens": None, "image_per_tile": None,
            "audio_input_per_minute": None, "audio_output_per_character": None,
            "embedding_per_m_tokens": per_m(v.get("input_cost_per_token")),
            "finetune_train_per_m_tokens": None,
            "finetune_input_per_m_tokens": None,
            "finetune_output_per_m_tokens": None,
        }
    else:
        audio_in_per_min = None
        if v.get("input_cost_per_second") is not None:
            audio_in_per_min = round(v["input_cost_per_second"] * 60, 10)
        elif v.get("input_cost_per_audio_per_second") is not None:
            audio_in_per_min = round(v["input_cost_per_audio_per_second"] * 60, 10)

        pricing = {
            "input_per_m_tokens":           per_m(v.get("input_cost_per_token")),
            "output_per_m_tokens":          per_m(v.get("output_cost_per_token")),
            "cache_read_per_m_tokens":      per_m(v.get("cache_read_input_token_cost")),
            "cache_write_per_m_tokens":     per_m(v.get("cache_creation_input_token_cost")),
            "reasoning_per_m_tokens":       per_m(v.get("input_cost_per_reasoning_token")),
            "image_per_tile":               v.get("output_cost_per_image"),
            "audio_input_per_minute":       audio_in_per_min,
            "audio_output_per_character":   v.get("output_cost_per_character"),
            "embedding_per_m_tokens":       None,
            "finetune_train_per_m_tokens":  per_m(v.get("ft_input_cost_per_token")),
            "finetune_input_per_m_tokens":  per_m(v.get("ft_input_cost_per_token")),
            "finetune_output_per_m_tokens": per_m(v.get("ft_output_cost_per_token")),
        }

    caps = {
        "vision":              bool(v.get("supports_vision")),
        "audio_input":         bool(v.get("supports_audio_input")),
        "audio_output":        bool(v.get("supports_audio_output")),
        "function_calling":    bool(v.get("supports_function_calling")),
        "parallel_tool_calls": bool(v.get("supports_parallel_function_calling")),
        "json_mode":           bool(v.get("supports_function_calling")),
        "response_schema":     bool(v.get("supports_response_schema")),
        "prompt_caching":      bool(v.get("supports_prompt_caching")),
        "reasoning":           bool(v.get("supports_reasoning") or v.get("input_cost_per_reasoning_token")),
        "streaming":           True,
        "finetuneable":        bool(v.get("ft_input_cost_per_token") or v.get("ft_output_cost_per_token")),
    }

    lifecycle_status = "ga"
    if v.get("deprecation_date"):
        lifecycle_status = "deprecated"

    lifecycle = {
        "status":           lifecycle_status,
        "deprecation_date": v.get("deprecation_date"),
        "sunset_date":      None,
        "successor":        None,
    }

    return {
        "provider":          provider,
        "model_id":          model_id,
        "display_name":      v.get("display_name") or model_id,
        "mode":              mode,
        "context_window":    int(v.get("max_input_tokens") or 0),
        "max_output_tokens": int(v.get("max_output_tokens") or 0),
        "pricing":           pricing,
        "capabilities":      caps,
        "lifecycle":         lifecycle,
        "source":            v.get("source") or "",
        "updated_at":        "2026-02-28",
    }


def has_pricing(entry: dict) -> bool:
    p = entry["pricing"]
    return any(
        p.get(k) is not None
        for k in ("input_per_m_tokens", "output_per_m_tokens",
                  "embedding_per_m_tokens", "image_per_tile",
                  "audio_input_per_minute", "audio_output_per_character")
    )


def main():
    repo_root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    src = os.path.join(repo_root, "model_prices_and_context_window.json")
    dst = os.path.join(repo_root, "models", "catalog.json")
    backup = os.path.join(repo_root, "models", "catalog_backup.json")

    print(f"Reading {src} …", file=sys.stderr)
    with open(src, encoding="utf-8") as f:
        raw = json.load(f)

    catalog = {}
    counters = {"skipped_spec": 0, "skipped_mode": 0, "skipped_no_provider": 0,
                "duplicates": 0, "written": 0}

    for litellm_key, v in raw.items():
        if litellm_key == "sample_spec":
            counters["skipped_spec"] += 1
            continue
        if not isinstance(v, dict):
            continue

        litellm_provider = v.get("litellm_provider") or ""
        if not litellm_provider:
            counters["skipped_no_provider"] += 1
            continue

        provider = canonical_provider(litellm_provider)
        entry = convert_entry(litellm_key, v, provider)
        if entry is None:
            counters["skipped_mode"] += 1
            continue

        catalog_key = provider + "/" + entry["model_id"]
        if catalog_key in catalog:
            counters["duplicates"] += 1
            if has_pricing(entry) and not has_pricing(catalog[catalog_key]):
                catalog[catalog_key] = entry
        else:
            catalog[catalog_key] = entry
            counters["written"] += 1

    print(
        f"  {counters['written']} entries written\n"
        f"  {counters['duplicates']} duplicates resolved (kept richer entry)\n"
        f"  {counters['skipped_mode']} skipped (unsupported mode)\n"
        f"  {counters['skipped_no_provider']} skipped (no litellm_provider field)",
        file=sys.stderr,
    )

    with open(dst, "w", encoding="utf-8") as f:
        json.dump(catalog, f, indent=2, ensure_ascii=False)
        f.write("\n")

    shutil.copy2(dst, backup)
    print(f"\nWritten → {dst}", file=sys.stderr)
    print(f"Copied  → {backup}", file=sys.stderr)


if __name__ == "__main__":
    main()
