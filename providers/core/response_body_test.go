package core

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadResponseBody_UnderCap(t *testing.T) {
	body, err := ReadResponseBody(bytes.NewReader([]byte("hello")), 10)
	if err != nil {
		t.Fatalf("ReadResponseBody: %v", err)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

func TestReadResponseBody_ExactlyAtCap(t *testing.T) {
	body, err := ReadResponseBody(bytes.NewReader([]byte("hello")), 5)
	if err != nil {
		t.Fatalf("ReadResponseBody: %v", err)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

func TestReadResponseBody_OverCap(t *testing.T) {
	_, err := ReadResponseBody(bytes.NewReader([]byte("hello world")), 5)
	if err == nil {
		t.Fatal("expected error for a body exceeding the cap")
	}
	if !strings.Contains(err.Error(), "5 byte limit") {
		t.Errorf("error = %q, want it to mention the byte limit", err.Error())
	}
}

func TestReadResponseBody_Empty(t *testing.T) {
	body, err := ReadResponseBody(bytes.NewReader(nil), 10)
	if err != nil {
		t.Fatalf("ReadResponseBody: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want empty", body)
	}
}
