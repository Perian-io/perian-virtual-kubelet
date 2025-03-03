package main

import (
	"context"

	perian "github.com/Perian-io/perian-virtual-kubelet/src"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	perian.RunPerianVirtualKubelet(ctx)
}
