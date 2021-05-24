
all: kind nats cluster image deploy

kind:
ifeq (, $(shell which protoc))
	cd .. && GO111MODULE=on go get sigs.k8s.io/kind@v0.11.0
endif
	kind create cluster
	sleep 10

nats:
	kubectl apply -f https://github.com/nats-io/nats-operator/releases/latest/download/00-prereqs.yaml --context kind-kind
	sleep 3
	kubectl apply -f https://github.com/nats-io/nats-operator/releases/latest/download/10-deployment.yaml --context kind-kind
	sleep 45
	kubectl apply -f https://raw.githubusercontent.com/nats-io/nats-streaming-operator/main/deploy/default-rbac.yaml --context kind-kind
	sleep 3
	kubectl apply -f https://raw.githubusercontent.com/nats-io/nats-streaming-operator/main/deploy/deployment.yaml --context kind-kind
	sleep 30

cluster:
	kubectl apply -k ./k8s/nats --context kind-kind
	sleep 15

proto: 
ifeq (, $(shell which protoc))
	$(error "No protoc in $$PATH, visit https://grpc.io/docs/protoc-installation/")
endif
ifeq (, $(shell which protoc-gen-go))
	go get google.golang.org/protobuf/cmd/protoc-gen-go@v1.26
	go get google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.1
endif
	rm -rf ./hello
	mkdir -p hello
	protoc --go_out=./hello --go_opt=paths=source_relative \
		--go-grpc_out=./hello \
		--go-grpc_opt=paths=source_relative ./hello.proto

app: proto
	go build

image:
ifeq (, $(shell which kind))
	$(error "No kind in $$PATH, visit https://kind.sigs.k8s.io")
endif
	docker build -t app:local .
	kind load docker-image app:local

deploy:
	kubectl apply -k ./k8s/app --context kind-kind
