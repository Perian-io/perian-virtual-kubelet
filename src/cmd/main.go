package main

import (
	"context"

	perian "github.com/Perian-io/perian-virtual-kubelet/src/kubelet"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	perian.RunPerianVirtualKubelet(ctx)
}
