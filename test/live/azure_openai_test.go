//go:build live

package live_test

import "testing"

func TestLive_AzureOpenAI_Chat(t *testing.T)          { liveChat(t, "azure-openai") }
func TestLive_AzureOpenAI_Stream(t *testing.T)        { liveStream(t, "azure-openai") }
func TestLive_AzureOpenAI_Embed(t *testing.T)         { liveEmbed(t, "azure-openai") }
func TestLive_AzureOpenAI_Image(t *testing.T)         { liveImage(t, "azure-openai") }
func TestLive_AzureOpenAI_Transcription(t *testing.T) { liveTranscribe(t, "azure-openai") }
func TestLive_AzureOpenAI_Speech(t *testing.T)        { liveSpeak(t, "azure-openai") }
