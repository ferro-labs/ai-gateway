//go:build live

package live_test

import "testing"

func TestLive_AI21_Chat(t *testing.T)   { liveChat(t, "ai21") }
func TestLive_AI21_Stream(t *testing.T) { liveStream(t, "ai21") }
