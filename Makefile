build:
	OOS=linux go build -o bin/vk src/cmd/virtual-kubelet/main.go

build-docker:
	docker build docker/. -t perianvk:latest
