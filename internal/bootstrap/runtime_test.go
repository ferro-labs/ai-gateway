package bootstrap

import (
	"context"
	"reflect"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/httpserver"
)

type orderedCloser struct {
	name  string
	order *[]string
}

func (c *orderedCloser) Close() error {
	*c.order = append(*c.order, c.name)
	return nil
}

func TestCloseRuntimeResourcesFlushesOTelLast(t *testing.T) {
	var order []string
	resources := []httpserver.NamedResource{
		{Name: "first", Value: &orderedCloser{name: "first", order: &order}},
		{Name: "second", Value: &orderedCloser{name: "second", order: &order}},
	}
	shutdown := func(context.Context) error {
		order = append(order, "otel")
		return nil
	}

	if err := closeRuntimeResources(resources, shutdown); err != nil {
		t.Fatalf("closeRuntimeResources returned an error: %v", err)
	}
	if want := []string{"first", "second", "otel"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("close order = %v, want %v", order, want)
	}
}
