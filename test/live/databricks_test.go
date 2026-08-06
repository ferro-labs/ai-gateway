//go:build live

package live_test

import "testing"

func TestLive_Databricks_Chat(t *testing.T)   { liveChat(t, "databricks") }
func TestLive_Databricks_Stream(t *testing.T) { liveStream(t, "databricks") }
func TestLive_Databricks_Embed(t *testing.T)  { liveEmbed(t, "databricks") }
