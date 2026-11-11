package unattended

import (
	"context"
	"testing"
)

func TestMark(t *testing.T) {
	if Is(context.Background()) || !Is(With(context.Background())) {
		t.Fatal("mark")
	}
}
