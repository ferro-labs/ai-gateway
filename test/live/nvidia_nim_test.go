//go:build live

package live_test

import "testing"

func TestLive_NvidiaNIM_Chat(t *testing.T)      { liveChat(t, "nvidia-nim") }
func TestLive_NvidiaNIM_Stream(t *testing.T)    { liveStream(t, "nvidia-nim") }
func TestLive_NvidiaNIM_Embed(t *testing.T)     { liveEmbed(t, "nvidia-nim") }
func TestLive_NvidiaNIM_Discovery(t *testing.T) { liveDiscovery(t, "nvidia-nim") }
func TestLive_NvidiaNIM_Rerank(t *testing.T)    { liveRerank(t, "nvidia-nim") }
