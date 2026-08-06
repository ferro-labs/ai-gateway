//go:build live

package live_test

import "testing"

func TestLive_AzureFoundry_Chat(t *testing.T)   { liveChat(t, "azure-foundry") }
func TestLive_AzureFoundry_Stream(t *testing.T) { liveStream(t, "azure-foundry") }
func TestLive_AzureFoundry_Embed(t *testing.T)  { liveEmbed(t, "azure-foundry") }
