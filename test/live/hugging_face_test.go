//go:build live

package live_test

import "testing"

func TestLive_HuggingFace_Chat(t *testing.T)      { liveChat(t, "hugging-face") }
func TestLive_HuggingFace_Stream(t *testing.T)    { liveStream(t, "hugging-face") }
func TestLive_HuggingFace_Embed(t *testing.T)     { liveEmbed(t, "hugging-face") }
func TestLive_HuggingFace_Image(t *testing.T)     { liveImage(t, "hugging-face") }
func TestLive_HuggingFace_Discovery(t *testing.T) { liveDiscovery(t, "hugging-face") }
