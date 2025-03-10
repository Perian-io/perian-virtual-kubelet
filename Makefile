build:
	OOS=linux go build -o bin/vk src/cmd/main.go

build-docker:
	docker build -f docker/Dockerfile -t perian-virtual-kubelet:latest .

test:
	go test src/test/*
